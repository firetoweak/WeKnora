package service

import (
	"context"
	"errors"
	"strings"
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
	pageUpdates []string
	moves       map[string]string
	bodyErr     error
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

func (s *titleCollapseWikiService) UpdatePage(
	_ context.Context, page *types.WikiPage,
) (*types.WikiPage, error) {
	s.pageUpdates = append(s.pageUpdates, page.Slug)
	return page, nil
}

func (s *titleCollapseWikiService) UpdateAutoLinkedContent(
	_ context.Context, page *types.WikiPage,
) error {
	s.bodyUpdates = append(s.bodyUpdates, page.Slug)
	return s.bodyErr
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
		InLinks:    types.StringArray{"concept/referrer"},
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
	model := &templateCaptureChatModel{
		response: "SUMMARY: Consolidated Acme information\n# Acme\n\nMerged from doc-1 and doc-3.",
	}
	groups := findDuplicateTitleGroups(fake.pages)

	collapsed, canonical, err := svc.collapseDuplicateTitlePages(
		context.Background(), model, "kb-1", "English", "", fake.pages, groups,
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
		!containsTestString(winner.Aliases, loser.Title) {
		t.Fatalf("winner metadata was not merged: refs=%v chunks=%v aliases=%v",
			winner.SourceRefs, winner.ChunkRefs, winner.Aliases)
	}
	if winner.Summary != "Consolidated Acme information" ||
		winner.Content != "# Acme\n\nMerged from doc-1 and doc-3." {
		t.Fatalf("winner body was not merged: summary=%q content=%q", winner.Summary, winner.Content)
	}
	if !strings.Contains(model.prompt, "doc-1, doc-2") || !strings.Contains(model.prompt, "doc-3") {
		t.Fatalf("merge prompt omitted source attribution context: %q", model.prompt)
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
	model := &templateCaptureChatModel{response: "SUMMARY: Same\n# Same\n\nMerged."}

	_, canonical, err := svc.collapseDuplicateTitlePages(
		context.Background(), model, "kb-1", "English", "", pages, findDuplicateTitleGroups(pages),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(canonical) != 1 || canonical[0] != "entity/richer" {
		t.Fatalf("canonical = %v", canonical)
	}
}

func TestCollapseDuplicateTitlePages_IncompleteModelOutputKeepsLosersLive(t *testing.T) {
	pages := []*types.WikiPage{
		{Slug: "entity/a", Title: "Same", PageType: types.WikiPageTypeEntity, Status: types.WikiPageStatusPublished},
		{Slug: "entity/b", Title: "same", PageType: types.WikiPageTypeEntity, Status: types.WikiPageStatusPublished},
	}
	fake := &titleCollapseWikiService{pages: pages}
	svc := &wikiIngestService{wikiService: fake}
	model := &templateCaptureChatModel{response: "SUMMARY: missing body"}

	collapsed, canonical, err := svc.collapseDuplicateTitlePages(
		context.Background(), model, "kb-1", "English", "", pages, findDuplicateTitleGroups(pages),
	)
	if err == nil {
		t.Fatal("expected incomplete model output error")
	}
	if collapsed != 0 || len(canonical) != 0 || len(fake.metaUpdates) != 0 || len(fake.pageUpdates) != 0 {
		t.Fatalf("unsafe partial merge: collapsed=%d canonical=%v meta=%v pages=%v",
			collapsed, canonical, fake.metaUpdates, fake.pageUpdates)
	}
	for _, page := range pages {
		if page.Status == types.WikiPageStatusArchived {
			t.Fatalf("page %s was archived after model failure", page.Slug)
		}
	}
}

func TestCollapseDuplicateTitlePages_LinkFailureKeepsLosersLive(t *testing.T) {
	winner := &types.WikiPage{
		Slug: "entity/a", Title: "Same", PageType: types.WikiPageTypeEntity,
		Status: types.WikiPageStatusPublished,
	}
	loser := &types.WikiPage{
		Slug: "entity/b", Title: "same", PageType: types.WikiPageTypeEntity,
		Status: types.WikiPageStatusPublished, InLinks: types.StringArray{"concept/ref"},
	}
	referrer := &types.WikiPage{
		Slug: "concept/ref", Title: "Ref", PageType: types.WikiPageTypeConcept,
		Status: types.WikiPageStatusPublished, Content: "See [[entity/b]].",
	}
	pages := []*types.WikiPage{winner, loser, referrer}
	fake := &titleCollapseWikiService{pages: pages, bodyErr: errors.New("database unavailable")}
	svc := &wikiIngestService{wikiService: fake}
	model := &templateCaptureChatModel{response: "SUMMARY: Merged\n# Same\n\nMerged."}

	collapsed, canonical, err := svc.collapseDuplicateTitlePages(
		context.Background(), model, "kb-1", "English", "", pages, findDuplicateTitleGroups(pages),
	)
	if err == nil {
		t.Fatal("expected link update failure")
	}
	if collapsed != 0 || len(canonical) != 0 || loser.Status == types.WikiPageStatusArchived {
		t.Fatalf("loser archived despite link failure: collapsed=%d canonical=%v status=%q",
			collapsed, canonical, loser.Status)
	}
	if len(fake.metaUpdates) != 0 {
		t.Fatalf("archive was attempted after link failure: %v", fake.metaUpdates)
	}
}

func TestFindDuplicateTitleGroupsIgnoresNonKnowledgePages(t *testing.T) {
	pages := []*types.WikiPage{
		{Slug: "entity/a", Title: " RAG ", PageType: types.WikiPageTypeEntity},
		{Slug: "concept/b", Title: "rag", PageType: types.WikiPageTypeConcept},
		{Slug: "summary/c", Title: "RAG", PageType: types.WikiPageTypeSummary},
		{Slug: "entity/d", Title: "Other", PageType: types.WikiPageTypeEntity},
	}
	groups := findDuplicateTitleGroups(pages)
	if len(groups) != 1 || groups[0].key != "rag" || len(groups[0].pages) != 2 {
		t.Fatalf("groups = %+v", groups)
	}
}

func TestCollapseDuplicateTitlePagesCallsModelOncePerGroup(t *testing.T) {
	pages := []*types.WikiPage{
		{Slug: "entity/a1", Title: "Alpha", PageType: types.WikiPageTypeEntity, Status: types.WikiPageStatusPublished},
		{Slug: "entity/a2", Title: " alpha ", PageType: types.WikiPageTypeEntity, Status: types.WikiPageStatusPublished},
		{Slug: "concept/b1", Title: "Beta", PageType: types.WikiPageTypeConcept, Status: types.WikiPageStatusPublished},
		{Slug: "concept/b2", Title: "BETA", PageType: types.WikiPageTypeConcept, Status: types.WikiPageStatusPublished},
	}
	fake := &titleCollapseWikiService{pages: pages}
	svc := &wikiIngestService{wikiService: fake}
	model := &templateCaptureChatModel{response: "SUMMARY: Merged\n# Merged\n\nContent."}

	collapsed, _, err := svc.collapseDuplicateTitlePages(
		context.Background(), model, "kb-1", "English", "", pages, findDuplicateTitleGroups(pages),
	)
	if err != nil {
		t.Fatal(err)
	}
	if model.calls != 2 || collapsed != 2 {
		t.Fatalf("model calls=%d collapsed=%d, want one call and one archive per duplicate group", model.calls, collapsed)
	}
}

func containsTestString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
