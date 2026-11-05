package app

import (
	"container/list"
	"maps"
	"sync"

	"duplex/internal/catalog"
	"duplex/internal/library"
)

const (
	metadataArtifactEntries = 1024
	metadataArtifactBytes   = int64(8 << 20)
)

// Catalogue reads remain authoritative. This cache retains only immutable NFO
// bytes and compares their full input before reuse, including playlist order.
type metadataArtifacts struct {
	mu    sync.Mutex
	items map[string]*list.Element
	order list.List
	bytes int64
}

type metadataArtifact struct {
	key   string
	input catalog.Episode
	entry library.Entry
	cost  int64
	names map[int]string
}

func (a *App) episodeMetadata(ep catalog.Episode) (library.Entry, error) {
	key := library.EpisodeBase(ep.OwnerID, ep.Season, ep.FileNumber)
	modified := ep.Updated

	ep.Updated = 0
	if entry, ok := a.nfoCache.get(key, ep, nil); ok {
		a.Measurements.Count("filesystem.nfo.hit", 1)

		entry.Modified = modified

		return entry, nil
	}

	entry, err := library.EpisodeMetadata(libraryEpisode(ep))
	if err != nil {
		return library.Entry{}, err
	}

	a.Measurements.Count("filesystem.nfo.render", 1)
	a.nfoCache.put(metadataArtifact{key: key, input: ep, entry: entry})
	entry.Modified = modified

	return entry, nil
}

func (a *App) showMetadataArtifact(channel, title string, names map[int]string, modified int64) (library.Entry, error) {
	key := "show:" + channel

	input := catalog.Episode{OwnerID: channel, Title: title}
	if entry, ok := a.nfoCache.get(key, input, names); ok {
		a.Measurements.Count("filesystem.nfo.hit", 1)

		entry.Modified = modified

		return entry, nil
	}

	entry, err := library.ShowMetadata(channel, title, names, modified)
	if err != nil {
		return library.Entry{}, err
	}

	a.Measurements.Count("filesystem.nfo.render", 1)
	a.nfoCache.put(metadataArtifact{key: key, input: input, names: names, entry: entry})

	return entry, nil
}

func (c *metadataArtifacts) get(key string, input catalog.Episode, names map[int]string) (library.Entry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	element := c.items[key]
	if element == nil {
		return library.Entry{}, false
	}

	value, ok := element.Value.(metadataArtifact)
	if !ok || value.input != input || !maps.Equal(value.names, names) {
		return library.Entry{}, false
	}

	c.order.MoveToFront(element)

	return value.entry, true
}

func (c *metadataArtifacts) put(value metadataArtifact) {
	value.cost = artifactCost(value)

	c.mu.Lock()
	defer c.mu.Unlock()

	if old := c.items[value.key]; old != nil {
		if !c.remove(old) {
			return
		}
	}

	if value.cost > metadataArtifactBytes {
		return
	}

	value.names = maps.Clone(value.names)

	if c.items == nil {
		c.items = make(map[string]*list.Element)
	}

	for c.order.Len() >= metadataArtifactEntries || c.bytes+value.cost > metadataArtifactBytes {
		if !c.remove(c.order.Back()) {
			return
		}
	}

	c.items[value.key] = c.order.PushFront(value)
	c.bytes += value.cost
}

func artifactCost(value metadataArtifact) int64 {
	cost := value.entry.Media.Size() + int64(len(value.key)+len(value.entry.Path)) + 1024
	for _, text := range []string{value.input.OwnerID, value.input.ChannelTitle, value.input.VideoID, value.input.Title, value.input.Description, value.input.PlaylistID, value.input.SeasonTitle, value.input.OccurrenceID} {
		cost += int64(len(text))
	}

	for _, title := range value.names {
		cost += int64(len(title)) + 32
	}

	return cost
}

func (c *metadataArtifacts) remove(element *list.Element) bool {
	value, ok := element.Value.(metadataArtifact)
	if !ok {
		return false
	}

	delete(c.items, value.key)
	c.bytes -= value.cost
	c.order.Remove(element)

	return true
}
