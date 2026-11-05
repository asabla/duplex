package app

import (
	"context"
	"io/fs"
	"path"
	"sort"
	"strings"

	"duplex/internal/catalog"
	"duplex/internal/library"
)

// ReadDirAttributes supplies one season page from one episode query. Other
// directory types retain their current timestamp and membership checks.
func (p catalogueFS) ReadDirAttributes(ctx context.Context, name, after string, limit int) ([]library.Child, error) {
	parts := strings.Split(name, "/")
	if len(parts) != 2 || !strings.HasPrefix(parts[1], "Season ") {
		return p.ReadDir(ctx, name, after, limit)
	}

	defer p.app.Measurements.Begin("filesystem.readdir")()

	if limit < 1 || limit > 256 {
		return nil, fs.ErrInvalid
	}

	if err := p.checkDirectory(ctx, name); err != nil {
		return nil, err
	}

	ch, err := p.channel(parts[0])
	if err != nil {
		return nil, err
	}

	season, err := seasonNumber(parts[1])
	if err != nil {
		return nil, err
	}

	visible := libraryVisibility(ch)
	base := ""

	if after != "" && after != ".plexmatch" {
		number, _, err := episodeNumber(ch.ID, season, after)
		if err != nil {
			return nil, err
		}

		base = path.Base(library.EpisodeBase(ch.ID, season, number))
	}

	episodes, err := p.app.Catalog.ProjectionEntryDetails(ctx, visible, ch.ID, season, base, (limit+1)/2+1)
	if err != nil {
		return nil, err
	}

	return p.episodeAttributes(episodes, after, limit)
}

func (p catalogueFS) episodeAttributes(episodes []catalog.Episode, after string, limit int) ([]library.Child, error) {
	children := make([]library.Child, 0, limit+2)
	if after < ".plexmatch" && len(episodes) > 0 && episodes[0].PlaylistID != "" {
		children = append(children, library.Child{Name: ".plexmatch"})
	}

	for _, ep := range episodes {
		base := path.Base(library.EpisodeBase(ep.OwnerID, ep.Season, ep.FileNumber))
		if base+".mp4" > after {
			info := library.Node{Size: ep.Size, Modified: ep.Updated, Revision: ep.Revision}
			children = append(children, library.Child{Name: base + ".mp4", Info: &info})
		}

		if base+".nfo" > after {
			entry, err := p.app.episodeMetadata(ep)
			if err != nil {
				return nil, err
			}

			children = append(children, attributeChild(base+".nfo", entry, ep.Updated))
		}

		for _, asset := range p.app.loadAssets(ep.VideoID) {
			if base+asset.Path > after {
				children = append(children, attributeChild(base+asset.Path, asset, ep.Updated))
			}
		}

		if len(children) >= limit {
			break
		}
	}

	sort.Slice(children, func(i, j int) bool { return children[i].Name < children[j].Name })

	return children[:min(limit, len(children))], nil
}

func attributeChild(name string, entry library.Entry, modified int64) library.Child {
	info := library.Node{Size: entry.Media.Size(), Revision: entry.Media.ID(), Modified: max(entry.Modified, modified)}
	return library.Child{Name: name, Info: &info}
}
