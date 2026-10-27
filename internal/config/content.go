package config

import (
	"errors"
	"regexp"
)

// ContentPolicy selects source content independently. Nil retains the shared
// indexing policy used by existing configuration files.
type ContentPolicy struct {
	Uploads Selection `yaml:"uploads" json:"uploads"`
	Shorts  Selection `yaml:"shorts" json:"shorts"`
	Replays Selection `yaml:"replays" json:"replays"`
}

// ContentSelection resolves a source content kind under shared or separate rules.
func (c Channel) ContentSelection(kind string) Selection {
	if c.Content == nil {
		return c.Selection()
	}

	switch kind {
	case "upload":
		return c.Content.Uploads
	case "short":
		return c.Content.Shorts
	case "replay":
		return c.Content.Replays
	default:
		return Selection{Mode: "skip"}
	}
}

var videoIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)

func (c Channel) validateContent() error {
	if c.Content != nil {
		for _, policy := range []Selection{c.Content.Uploads, c.Content.Shorts, c.Content.Replays} {
			if policy.Mode == "inherit" {
				return errors.New("content selection must be latest, all, or skip")
			}

			if err := policy.validate(true); err != nil {
				return err
			}
		}
	}

	for id, mode := range c.VideoOverrides {
		if !videoIDPattern.MatchString(id) || (mode != "keep" && mode != "exclude") {
			return errors.New("video overrides require an eleven-character video ID and keep or exclude")
		}
	}

	return nil
}

// ValidateSelection checks the channel recording rules without storage I/O.
func (c Channel) ValidateSelection() error { return c.validatePolicy() }
