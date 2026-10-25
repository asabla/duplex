package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"

	"duplex/internal/catalog"
	"duplex/internal/config"
	"duplex/internal/library"
	"duplex/internal/media"
)

type (
	catalogueFS  struct{ app *App }
	resolvedFile struct {
		info    library.Node
		data    media.Representation
		episode *catalog.Episode
	}
)

func (p catalogueFS) Stat(ctx context.Context, name string) (library.Node, error) {
	file, err := p.lookup(ctx, name)
	return file.info, err
}

func (p catalogueFS) Open(ctx context.Context, name string) (media.Representation, int64, error) {
	file, err := p.lookup(ctx, name)
	if err != nil {
		return nil, 0, err
	}
	if file.info.Directory {
		return nil, 0, fs.ErrInvalid
	}
	if file.episode != nil {
		rep, err := p.app.bindEpisode(ctx, *file.episode)
		return rep, file.info.Modified, err
	}
	return file.data, file.info.Modified, nil
}

func (p catalogueFS) channel(name string) (config.Channel, error) {
	id, ok := strings.CutPrefix(name, "Channel-")
	if !ok {
		return config.Channel{}, fs.ErrNotExist
	}
	ch, err := p.app.followed(id)
	if err != nil {
		return ch, fs.ErrNotExist
	}
	return ch, nil
}

func cleanLibraryPath(name string) bool {
	return name == "" || (path.Clean(name) == name && !strings.HasPrefix(name, "/") && name != "." && name != ".." && !strings.HasPrefix(name, "../"))
}

func projectionError(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return fs.ErrNotExist
	}
	return err
}

func (p catalogueFS) lookup(ctx context.Context, name string) (resolvedFile, error) {
	if !cleanLibraryPath(name) {
		return resolvedFile{}, fs.ErrNotExist
	}
	if name == "" {
		return resolvedFile{info: library.Node{Directory: true, Modified: p.app.Config.Snapshot().LoadedAt.Unix()}}, ctx.Err()
	}
	parts := strings.Split(name, "/")
	if len(parts) > 3 {
		return resolvedFile{}, fs.ErrNotExist
	}
	ch, err := p.channel(parts[0])
	if err != nil {
		return resolvedFile{}, err
	}
	visible := visibility(config.Config{Channels: []config.Channel{ch}})
	if len(parts) == 1 {
		modified, err := p.app.Catalog.ProjectionChanged(ctx, visible, ch.ID)
		return resolvedFile{info: library.Node{Directory: true, Modified: modified}}, projectionError(err)
	}
	if len(parts) == 2 {
		return p.channelItem(ctx, ch, visible, parts[1])
	}
	return p.episodeItem(ctx, ch, visible, parts[1], parts[2])
}

func (p catalogueFS) episodeItem(ctx context.Context, ch config.Channel, visible catalog.Visibility, seasonName, fileName string) (resolvedFile, error) {
	season, err := seasonNumber(seasonName)
	if err != nil {
		return resolvedFile{}, err
	}
	number, suffix, err := episodeNumber(ch.ID, season, fileName)
	if err != nil {
		return resolvedFile{}, err
	}
	episodes, err := p.app.Catalog.ProjectionPage(ctx, visible, ch.ID, season, number-1, 1)
	if err != nil {
		return resolvedFile{}, err
	}
	if len(episodes) == 0 || episodes[0].Number != number {
		return resolvedFile{}, fs.ErrNotExist
	}
	ep := episodes[0]
	if suffix == ".mp4" {
		return resolvedFile{info: library.Node{Size: ep.Size, Modified: ep.Updated}, episode: &ep}, nil
	}
	if suffix == ".nfo" {
		entry, err := library.EpisodeMetadata(libraryEpisode(ep))
		return entryFile(entry), err
	}
	for _, entry := range p.app.loadAssets(ep.VideoID) {
		if entry.Path == suffix {
			entry.Modified = max(entry.Modified, ep.Updated)
			return entryFile(entry), nil
		}
	}
	return resolvedFile{}, fs.ErrNotExist
}

func libraryEpisode(ep catalog.Episode) library.Episode {
	return library.Episode{ChannelID: ep.OwnerID, ChannelTitle: ep.ChannelTitle, VideoID: ep.VideoID, Title: ep.Title, Description: ep.Description, Season: ep.Season, Number: ep.Number, SeasonTitle: ep.SeasonTitle, OccurrenceID: ep.OccurrenceID, Modified: ep.Updated}
}

func entryFile(entry library.Entry) resolvedFile {
	if entry.Media == nil {
		return resolvedFile{}
	}
	return resolvedFile{info: library.Node{Size: entry.Media.Size(), Modified: entry.Modified}, data: entry.Media}
}

func seasonNumber(name string) (int, error) {
	value, ok := strings.CutPrefix(name, "Season ")
	number, err := strconv.Atoi(value)
	if !ok || err != nil || number < 1 || fmt.Sprintf("Season %04d", number) != name {
		return 0, fs.ErrNotExist
	}
	return number, nil
}

func episodeNumber(channel string, season int, name string) (int, string, error) {
	prefix := fmt.Sprintf("Channel-%s - S%04dE", channel, season)
	value, ok := strings.CutPrefix(name, prefix)
	digits, suffix, found := strings.Cut(value, ".")
	number, err := strconv.Atoi(digits)
	if !ok || !found || err != nil || number < 1 || fmt.Sprintf("%04d", number) != digits {
		return 0, "", fs.ErrNotExist
	}
	return number, "." + suffix, nil
}

func (p catalogueFS) channelItem(ctx context.Context, ch config.Channel, visible catalog.Visibility, name string) (resolvedFile, error) {
	if name == "tvshow.nfo" {
		entry, err := p.showMetadata(ctx, ch, visible)
		return entryFile(entry), err
	}
	if strings.HasPrefix(name, "Season ") {
		number, err := seasonNumber(name)
		if err != nil {
			return resolvedFile{}, err
		}
		season, err := p.app.Catalog.ProjectionSeasonAt(ctx, visible, ch.ID, number)
		return resolvedFile{info: library.Node{Directory: true, Modified: season.Modified}}, projectionError(err)
	}
	if name == "poster.jpg" {
		return p.poster(ctx, ch.ID, "show")
	}
	raw, ok := strings.CutPrefix(name, "season")
	raw, ending := strings.CutSuffix(raw, "-poster.jpg")
	number, err := strconv.Atoi(raw)
	if !ok || !ending || err != nil || number < 1 || fmt.Sprintf("season%02d-poster.jpg", number) != name {
		return resolvedFile{}, fs.ErrNotExist
	}
	season, err := p.app.Catalog.ProjectionSeasonAt(ctx, visible, ch.ID, number)
	if err != nil {
		return resolvedFile{}, projectionError(err)
	}
	return p.poster(ctx, ch.ID, season.Scope)
}

func (p catalogueFS) poster(ctx context.Context, channel, scope string) (resolvedFile, error) {
	preview, err := p.app.Artwork(ctx, channel, scope)
	if err != nil {
		return resolvedFile{}, err
	}
	if preview.Path == "" {
		return resolvedFile{}, fs.ErrNotExist
	}
	stat, err := os.Stat(preview.Path)
	if err != nil {
		return resolvedFile{}, err
	}
	return entryFile(library.Entry{Media: localAsset{file: preview.Path, size: stat.Size()}, Modified: stat.ModTime().Unix()}), nil
}

func (p catalogueFS) showMetadata(ctx context.Context, ch config.Channel, visible catalog.Visibility) (library.Entry, error) {
	names := map[int]string{}
	after := ""
	for {
		seasons, err := p.app.Catalog.ProjectionSeasons(ctx, visible, ch.ID, after, 128)
		if err != nil {
			return library.Entry{}, err
		}
		for _, season := range seasons {
			if season.Title != "" {
				names[season.Number] = season.Title
			}
			after = season.Name
		}
		if len(seasons) < 128 {
			break
		}
	}
	title, _, _, err := p.app.Catalog.Branding(ctx, ch.ID)
	if err != nil {
		return library.Entry{}, err
	}
	modified, err := p.app.Catalog.ProjectionChanged(ctx, visible, ch.ID)
	if err != nil {
		return library.Entry{}, projectionError(err)
	}
	return library.ShowMetadata(ch.ID, title, names, modified)
}

func (p catalogueFS) ReadDir(ctx context.Context, name, after string, limit int) ([]library.Child, error) {
	if limit < 1 || limit > 256 {
		return nil, fs.ErrInvalid
	}
	info, err := p.Stat(ctx, name)
	if err != nil {
		return nil, err
	}
	if !info.Directory {
		return nil, fs.ErrInvalid
	}
	var children []library.Child
	if name == "" {
		children, err = p.rootChildren(ctx, after)
	} else {
		parts := strings.Split(name, "/")
		ch, err := p.channel(parts[0])
		if err != nil {
			return nil, err
		}
		visible := visibility(config.Config{Channels: []config.Channel{ch}})
		if len(parts) == 1 {
			children, err = p.channelChildren(ctx, ch, visible, after, limit)
		} else {
			children, err = p.episodeChildren(ctx, ch, visible, parts[1], after, limit)
		}
		if err != nil {
			return nil, err
		}
	}
	if err != nil {
		return nil, err
	}
	sort.Slice(children, func(i, j int) bool { return children[i].Name < children[j].Name })
	return children[:min(limit, len(children))], nil
}

func (p catalogueFS) rootChildren(ctx context.Context, after string) ([]library.Child, error) {
	children := []library.Child{}
	for _, ch := range p.app.Config.Snapshot().Config.Channels {
		child := "Channel-" + ch.ID
		if !ch.Enabled || child <= after {
			continue
		}
		if _, err := p.Stat(ctx, child); errors.Is(err, fs.ErrNotExist) {
			continue
		} else if err != nil {
			return nil, err
		}
		children = append(children, library.Child{Name: child, Directory: true})
	}

	return children, nil
}

func (p catalogueFS) channelChildren(ctx context.Context, ch config.Channel, visible catalog.Visibility, after string, limit int) ([]library.Child, error) {
	result := []library.Child{}
	seasons, err := p.app.Catalog.ProjectionSeasons(ctx, visible, ch.ID, after, limit)
	if err != nil {
		return nil, err
	}
	for _, season := range seasons {
		result = append(result, library.Child{Name: season.Name, Directory: true})
	}
	posterAfter := after
	for len(result) < limit {
		posters, err := p.app.Catalog.ProjectionPosters(ctx, visible, ch.ID, posterAfter, limit)
		if err != nil {
			return nil, err
		}
		for _, poster := range posters {
			posterAfter = poster.Name
			if _, err := p.poster(ctx, ch.ID, poster.Scope); err == nil {
				result = append(result, library.Child{Name: poster.Name})
			} else if !errors.Is(err, fs.ErrNotExist) {
				p.app.operationFailure("season poster", err)
			}
		}
		if len(posters) < limit {
			break
		}
	}
	for _, name := range []string{"poster.jpg", "tvshow.nfo"} {
		if name <= after {
			continue
		}
		if _, err := p.channelItem(ctx, ch, visible, name); err == nil {
			result = append(result, library.Child{Name: name})
		} else if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	}
	return result, nil
}

func (p catalogueFS) episodeChildren(ctx context.Context, ch config.Channel, visible catalog.Visibility, seasonName, after string, limit int) ([]library.Child, error) {
	season, err := seasonNumber(seasonName)
	if err != nil {
		return nil, err
	}
	base := ""
	if after != "" {
		number, _, err := episodeNumber(ch.ID, season, after)
		if err != nil {
			return nil, err
		}
		base = path.Base(library.EpisodeBase(ch.ID, season, number))
	}
	episodes, err := p.app.Catalog.ProjectionFilesPage(ctx, visible, ch.ID, season, base, min(256, limit+1))
	if err != nil {
		return nil, err
	}
	children := []library.Child{}
	for _, ep := range episodes {
		name := path.Base(library.EpisodeBase(ep.OwnerID, ep.Season, ep.Number))
		suffixes := []string{".mp4", ".nfo"}
		for _, asset := range p.app.loadAssets(ep.VideoID) {
			suffixes = append(suffixes, asset.Path)
		}
		for _, suffix := range suffixes {
			if name+suffix > after {
				children = append(children, library.Child{Name: name + suffix})
			}
		}
	}
	return children, nil
}
