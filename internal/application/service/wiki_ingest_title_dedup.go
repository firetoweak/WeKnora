package service

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
)

// collapseDuplicateTitlePages converges legacy entity/concept pages that have
// the same normalized title. It preserves one canonical page, merges
// provenance metadata, rewrites incoming wiki links, and archives the losers.
func (s *wikiIngestService) collapseDuplicateTitlePages(
	ctx context.Context,
	kbID string,
) (int, []string, error) {
	if s.wikiService == nil {
		return 0, nil, nil
	}
	pages, err := s.wikiService.ListAllPages(ctx, kbID)
	if err != nil {
		return 0, nil, fmt.Errorf("list pages: %w", err)
	}

	groups := make(map[string][]*types.WikiPage)
	for _, page := range pages {
		if page == nil ||
			(page.PageType != types.WikiPageTypeEntity && page.PageType != types.WikiPageTypeConcept) {
			continue
		}
		key := normalizeWikiTitle(page.Title)
		if key != "" {
			groups[key] = append(groups[key], page)
		}
	}

	collapsed := 0
	var canonicalSlugs []string
	for _, group := range groups {
		if len(group) < 2 {
			continue
		}
		sort.SliceStable(group, func(i, j int) bool {
			if len(group[i].SourceRefs) != len(group[j].SourceRefs) {
				return len(group[i].SourceRefs) > len(group[j].SourceRefs)
			}
			if !group[i].CreatedAt.Equal(group[j].CreatedAt) {
				return group[i].CreatedAt.Before(group[j].CreatedAt)
			}
			return group[i].Slug < group[j].Slug
		})

		winner := group[0]
		losers := group[1:]
		var folderSource *types.WikiPage
		if winner.FolderID == "" {
			for _, loser := range losers {
				if loser.FolderID != "" {
					folderSource = loser
					break
				}
			}
		}
		for _, loser := range losers {
			winner.SourceRefs = appendUniqueStrings(winner.SourceRefs, loser.SourceRefs...)
			winner.ChunkRefs = appendUniqueStrings(winner.ChunkRefs, loser.ChunkRefs...)
			winner.Aliases = appendUniqueStrings(winner.Aliases, loser.Aliases...)
			winner.Aliases = appendUniqueStrings(winner.Aliases, loser.Slug)
		}
		if err := s.wikiService.UpdatePageMeta(ctx, winner); err != nil {
			return collapsed, canonicalSlugs, fmt.Errorf("merge duplicate title metadata into %s: %w", winner.Slug, err)
		}
		if folderSource != nil {
			moved, moveErr := s.wikiService.MovePage(ctx, kbID, winner.Slug, folderSource.FolderID)
			if moveErr != nil {
				return collapsed, canonicalSlugs, fmt.Errorf(
					"preserve folder %s while merging duplicate title into %s: %w",
					folderSource.FolderID, winner.Slug, moveErr,
				)
			}
			if moved != nil {
				winner = moved
			}
		}

		for _, page := range pages {
			if page == nil || page.Status == types.WikiPageStatusArchived || page.Content == "" {
				continue
			}
			updated := page.Content
			for _, loser := range losers {
				updated = strings.ReplaceAll(updated, "[["+loser.Slug+"]]", "[["+winner.Slug+"]]")
				updated = strings.ReplaceAll(updated, "[["+loser.Slug+"|", "[["+winner.Slug+"|")
			}
			if updated == page.Content {
				continue
			}
			page.Content = updated
			if err := s.wikiService.UpdateAutoLinkedContent(ctx, page); err != nil {
				return collapsed, canonicalSlugs, fmt.Errorf("rewrite duplicate-title links in %s: %w", page.Slug, err)
			}
		}

		for _, loser := range losers {
			loser.Status = types.WikiPageStatusArchived
			if err := s.wikiService.UpdatePageMeta(ctx, loser); err != nil {
				return collapsed, canonicalSlugs, fmt.Errorf("archive duplicate title page %s: %w", loser.Slug, err)
			}
			collapsed++
		}
		canonicalSlugs = append(canonicalSlugs, winner.Slug)
	}

	sort.Strings(canonicalSlugs)
	return collapsed, canonicalSlugs, nil
}

func appendUniqueStrings(dst types.StringArray, values ...string) types.StringArray {
	seen := make(map[string]bool, len(dst)+len(values))
	for _, value := range dst {
		seen[value] = true
	}
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		dst = append(dst, value)
		seen[value] = true
	}
	return dst
}
