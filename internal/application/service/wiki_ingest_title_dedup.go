package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/Tencent/WeKnora/internal/agent"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
)

type duplicateTitleGroup struct {
	key   string
	pages []*types.WikiPage
}

// findDuplicateTitleGroups detects every exact-title duplicate in one
// in-memory pass. Summary, folder, and index pages are intentionally excluded.
func findDuplicateTitleGroups(pages []*types.WikiPage) []duplicateTitleGroup {
	grouped := make(map[string][]*types.WikiPage)
	for _, page := range pages {
		if page == nil ||
			page.Status == types.WikiPageStatusArchived ||
			(page.PageType != types.WikiPageTypeEntity && page.PageType != types.WikiPageTypeConcept) {
			continue
		}
		if key := normalizeWikiTitle(page.Title); key != "" {
			grouped[key] = append(grouped[key], page)
		}
	}

	groups := make([]duplicateTitleGroup, 0, len(grouped))
	for key, pages := range grouped {
		if len(pages) > 1 {
			groups = append(groups, duplicateTitleGroup{key: key, pages: pages})
		}
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].key < groups[j].key })
	return groups
}

// collapseDuplicateTitlePages converges exact same-name entity/concept pages
// after normal page generation. Each duplicate group costs one LLM call; an
// LLM or write failure leaves that group's loser pages live for a later retry.
func (s *wikiIngestService) collapseDuplicateTitlePages(
	ctx context.Context,
	chatModel chat.Chat,
	kbID string,
	lang string,
	customInstructions string,
	pages []*types.WikiPage,
	groups []duplicateTitleGroup,
) (int, []string, error) {
	if s.wikiService == nil || len(groups) == 0 {
		return 0, nil, nil
	}
	if chatModel == nil {
		return 0, nil, errors.New("merge duplicate title pages: chat model is required")
	}

	pagesBySlug := make(map[string]*types.WikiPage, len(pages))
	for _, page := range pages {
		if page != nil {
			pagesBySlug[page.Slug] = page
		}
	}

	collapsed := 0
	var canonicalSlugs []string
	var mergeErrs []error
	for _, duplicate := range groups {
		group := duplicate.pages
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
		raw, err := s.generateWithTemplate(ctx, chatModel, agent.WikiExactTitleMergePrompt, map[string]string{
			"Pages":              renderDuplicateTitlePages(group),
			"Language":           lang,
			"CustomInstructions": customInstructions,
			"InstructionScope":   "wiki_content",
		})
		if err != nil {
			mergeErrs = append(mergeErrs, fmt.Errorf("merge duplicate title %q: %w", winner.Title, err))
			continue
		}
		summary, content := splitSummaryLine(raw)
		if summary == "" || content == "" {
			mergeErrs = append(mergeErrs, fmt.Errorf("merge duplicate title %q: model returned incomplete content", winner.Title))
			continue
		}

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
			if loser.Title != winner.Title {
				winner.Aliases = appendUniqueStrings(winner.Aliases, loser.Title)
			}
		}

		content = rewriteDuplicateTitleLinks(content, winner.Slug, losers)
		winner.Summary = summary
		winner.Content = content
		if _, err := s.wikiService.UpdatePage(ctx, winner); err != nil {
			mergeErrs = append(mergeErrs, fmt.Errorf("update merged duplicate title page %s: %w", winner.Slug, err))
			continue
		}

		referrers := make(map[string]struct{})
		for _, loser := range losers {
			for _, slug := range loser.InLinks {
				if slug != "" && slug != winner.Slug {
					referrers[slug] = struct{}{}
				}
			}
		}
		linkFailed := false
		for slug := range referrers {
			page := pagesBySlug[slug]
			if page == nil || page.Status == types.WikiPageStatusArchived || page.Content == "" {
				continue
			}
			updated := rewriteDuplicateTitleLinks(page.Content, winner.Slug, losers)
			if updated == page.Content {
				continue
			}
			page.Content = updated
			if err := s.wikiService.UpdateAutoLinkedContent(ctx, page); err != nil {
				mergeErrs = append(mergeErrs, fmt.Errorf("rewrite duplicate-title links in %s: %w", page.Slug, err))
				linkFailed = true
				break
			}
		}
		if linkFailed {
			continue
		}
		if folderSource != nil {
			if _, moveErr := s.wikiService.MovePage(ctx, kbID, winner.Slug, folderSource.FolderID); moveErr != nil {
				mergeErrs = append(mergeErrs, fmt.Errorf(
					"preserve folder %s while merging duplicate title into %s: %w",
					folderSource.FolderID, winner.Slug, moveErr,
				))
				continue
			}
		}

		archiveFailed := false
		for _, loser := range losers {
			loser.Status = types.WikiPageStatusArchived
			if err := s.wikiService.UpdatePageMeta(ctx, loser); err != nil {
				mergeErrs = append(mergeErrs, fmt.Errorf("archive duplicate title page %s: %w", loser.Slug, err))
				archiveFailed = true
				break
			}
			collapsed++
		}
		if !archiveFailed {
			canonicalSlugs = append(canonicalSlugs, winner.Slug)
		}
	}

	sort.Strings(canonicalSlugs)
	return collapsed, canonicalSlugs, errors.Join(mergeErrs...)
}

func renderDuplicateTitlePages(pages []*types.WikiPage) string {
	var buf strings.Builder
	for _, page := range pages {
		fmt.Fprintf(&buf, "<page slug=\"%s\" type=\"%s\">\n", xmlEscape(page.Slug), xmlEscape(page.PageType))
		fmt.Fprintf(&buf, "<title>%s</title>\n", xmlEscape(page.Title))
		fmt.Fprintf(&buf, "<summary>%s</summary>\n", xmlEscape(page.Summary))
		fmt.Fprintf(&buf, "<source_refs>%s</source_refs>\n", xmlEscape(strings.Join(page.SourceRefs, ", ")))
		fmt.Fprintf(&buf, "<content>%s</content>\n", xmlEscape(page.Content))
		buf.WriteString("</page>\n")
	}
	return buf.String()
}

func rewriteDuplicateTitleLinks(content, winnerSlug string, losers []*types.WikiPage) string {
	for _, loser := range losers {
		content = strings.ReplaceAll(content, "[["+loser.Slug+"]]", "[["+winnerSlug+"]]")
		content = strings.ReplaceAll(content, "[["+loser.Slug+"|", "[["+winnerSlug+"|")
	}
	return content
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
