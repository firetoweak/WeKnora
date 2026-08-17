package service

import (
	"context"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

type titleCollapseWikiService struct {
	interfaces.WikiPageService
	pages       []*types.WikiPage
	metaUpdates []string
	bodyUpdates []string
	moves       map[string]string
}

func (s *titleCollapseWikiService) ListAllPages(
	context.Context, string,
) ([]*types.WikiPage, error) {
	return s.pages, nil
}

func (s *titleCollapseWikiService) UpdatePageMeta(
	_ context.Context, page *types.WikiPage,
) error {
	s.metaUpdates = append(s.metaUpdates, page.Slug)
	return nil
}

func (s *titleCollapseWikiService) UpdateAutoLinkedContent(
	_ context.Context, page *types.WikiPage,
) error {
	s.bodyUpdates = append(s.bodyUpdates, page.Slug)
	return nil
}

func (s *titleCollapseWikiService) MovePage(
	_ context.Context, _ string, slug string, folderID string,
) (*types.WikiPage, error) {
	if s.moves == nil {
		s.moves = make(map[string]string)
	}
	s.moves[slug] = folderID
	for _, page := range s.pages {
		if page.Slug == slug {
			page.FolderID = folderID
			return page, nil
		}
	}
	return nil, nil
}

func TestCollapseDuplicateTitlePages(t *testing.T) {
	now := time.Now()
	winner := &types.WikiPage{
		Slug:       "entity/acme",
		Title:      "Acme",
		PageType:   types.WikiPageTypeEntity,
		Status:     types.WikiPageStatusPublished,
		SourceRefs: types.StringArray{"doc-1", "doc-2"},
		ChunkRefs:  types.StringArray{"chunk-1"},
		Aliases:    types.StringArray{"Acme Corp"},
		CreatedAt:  now,
	}
	loser := &types.WikiPage{
		Slug:       "concept/acme-duplicate",
		Title:      " acme ",
		PageType:   types.WikiPageTypeConcept,
		Status:     types.WikiPageStatusPublished,
		SourceRefs: types.StringArray{"doc-3"},
		ChunkRefs:  types.StringArray{"chunk-2"},
		Aliases:    types.StringArray{"ACME"},
		FolderID:   "folder-products",
		CreatedAt:  now.Add(-time.Hour),
	}
	referrer := &types.WikiPage{
		Slug:     "concept/referrer",
		Title:    "Referrer",
		PageType: types.WikiPageTypeConcept,
		Status:   types.WikiPageStatusPublished,
		Content:  "See [[concept/acme-duplicate]] and [[concept/acme-duplicate|Acme]].",
	}
	fake := &titleCollapseWikiService{pages: []*types.WikiPage{winner, loser, referrer}}
	svc := &wikiIngestService{wikiService: fake}

	collapsed, canonical, err := svc.collapseDuplicateTitlePages(
		context.Background(), "kb-1",
	)
	if err != nil {
		t.Fatalf("collapseDuplicateTitlePages: %v", err)
	}
	if collapsed != 1 || len(canonical) != 1 || canonical[0] != winner.Slug {
		t.Fatalf("collapsed=%d canonical=%v", collapsed, canonical)
	}
	if loser.Status != types.WikiPageStatusArchived {
		t.Fatalf("loser status = %q", loser.Status)
	}
	if winner.FolderID != loser.FolderID || fake.moves[winner.Slug] != loser.FolderID {
		t.Fatalf("winner folder was not preserved: winner=%q moves=%v", winner.FolderID, fake.moves)
	}
	if !containsTestString(winner.SourceRefs, "doc-3") ||
		!containsTestString(winner.ChunkRefs, "chunk-2") ||
		!containsTestString(winner.Aliases, loser.Slug) {
		t.Fatalf("winner metadata was not merged: refs=%v chunks=%v aliases=%v",
			winner.SourceRefs, winner.ChunkRefs, winner.Aliases)
	}
	want := "See [[entity/acme]] and [[entity/acme|Acme]]."
	if referrer.Content != want {
		t.Fatalf("rewritten content = %q, want %q", referrer.Content, want)
	}
}

func TestCollapseDuplicateTitlePages_WinnerUsesMostSources(t *testing.T) {
	older := time.Now().Add(-time.Hour)
	pages := []*types.WikiPage{
		{
			Slug:       "entity/older",
			Title:      "Same",
			PageType:   types.WikiPageTypeEntity,
			Status:     types.WikiPageStatusPublished,
			SourceRefs: types.StringArray{"doc-1"},
			CreatedAt:  older,
		},
		{
			Slug:       "entity/richer",
			Title:      "same",
			PageType:   types.WikiPageTypeEntity,
			Status:     types.WikiPageStatusPublished,
			SourceRefs: types.StringArray{"doc-1", "doc-2"},
			CreatedAt:  older.Add(time.Minute),
		},
	}
	fake := &titleCollapseWikiService{pages: pages}
	svc := &wikiIngestService{wikiService: fake}

	_, canonical, err := svc.collapseDuplicateTitlePages(context.Background(), "kb-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(canonical) != 1 || canonical[0] != "entity/richer" {
		t.Fatalf("canonical = %v", canonical)
	}
}
