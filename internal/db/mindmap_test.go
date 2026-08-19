package db

import (
	"context"
	"errors"
	"testing"

	"learnix/internal/mindmap"
)

func TestMindMapRepoRoundTripAndMissing(t *testing.T) {
	database, cleanup := NewTestDB(t)
	defer cleanup()
	ctx := context.Background()
	users := NewUserRepo(database)
	studies := NewStudyRepo(database)
	userID, err := users.Create(ctx, "map@repo.test", "hash")
	if err != nil {
		t.Fatal(err)
	}
	study := &Study{UserID: userID, Topic: "Álgebra", Phase: "study"}
	if err := studies.Create(ctx, study); err != nil {
		t.Fatal(err)
	}
	repo := NewMindMapRepo(database)
	if _, err := repo.Load(ctx, study.ID); !errors.Is(err, mindmap.ErrNotFound) {
		t.Fatalf("missing map error = %v, want ErrNotFound", err)
	}
	graph, err := mindmap.New(mindmap.Node{ID: "root", Label: "Álgebra"})
	if err != nil {
		t.Fatal(err)
	}
	graph, err = graph.AddNode(mindmap.Node{ID: "equacao", ParentID: "root", Label: "Equações"})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Save(ctx, study.ID, graph); err != nil {
		t.Fatal(err)
	}
	loaded, err := repo.Load(ctx, study.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := loaded.Node("equacao"); err != nil || got.Label != "Equações" {
		t.Fatalf("loaded graph = %+v, err=%v", got, err)
	}
	if err := repo.Save(ctx, study.ID, mindmap.Graph{}); err == nil {
		t.Fatal("invalid graph should not be persisted")
	}
}
