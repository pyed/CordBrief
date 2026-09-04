package catalog

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad_ValidCatalog(t *testing.T) {
	dir := t.TempDir()
	sample := `{
  "version": 1,
  "updated_at": "2026-09-04T00:00:00Z",
  "guilds": [
    {
      "id": "g1",
      "name": "Haskell Server",
      "channels": [
        {"id": "c1", "name": "general", "type": 0},
        {"id": "c2", "name": "announcements", "type": 5}
      ]
    }
  ]
}`
	if err := os.WriteFile(filepath.Join(dir, "catalog.json"), []byte(sample), 0644); err != nil {
		t.Fatal(err)
	}

	cat, err := Load(dir)
	if err != nil {
		t.Fatalf("unexpected load error: %v", err)
	}

	if cat.Version != 1 {
		t.Errorf("expected version 1, got %d", cat.Version)
	}
	if len(cat.Guilds) != 1 || cat.Guilds[0].Name != "Haskell Server" {
		t.Errorf("unexpected guilds: %+v", cat.Guilds)
	}
	if len(cat.Guilds[0].Channels) != 2 {
		t.Errorf("expected 2 channels, got %d", len(cat.Guilds[0].Channels))
	}

	// Test FindChannel
	ch, g, found := cat.FindChannel("c2")
	if !found || ch.Name != "announcements" || g.ID != "g1" {
		t.Errorf("FindChannel failed: found=%v, ch=%+v, g=%+v", found, ch, g)
	}

	// Test ValidateChannelIDs
	valid, invalid := cat.ValidateChannelIDs([]string{"c1", "c999"})
	if len(valid) != 1 || valid[0] != "c1" {
		t.Errorf("expected valid [c1], got %v", valid)
	}
	if len(invalid) != 1 || invalid[0] != "c999" {
		t.Errorf("expected invalid [c999], got %v", invalid)
	}
}

func TestLoad_NotFound(t *testing.T) {
	dir := t.TempDir()
	cat, err := Load(dir)
	if cat != nil || err != ErrCatalogNotFound {
		t.Fatalf("expected ErrCatalogNotFound, got cat=%v, err=%v", cat, err)
	}
}

func TestLoad_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "catalog.json"), []byte("{ bad json"), 0644)
	_, err := Load(dir)
	if err == nil {
		t.Fatal("expected error on bad json, got nil")
	}
}

func TestLoad_UnsupportedVersion(t *testing.T) {
	dir := t.TempDir()
	sample := `{"version": 99, "updated_at": "2026-09-04T00:00:00Z", "guilds": []}`
	_ = os.WriteFile(filepath.Join(dir, "catalog.json"), []byte(sample), 0644)
	_, err := Load(dir)
	if err == nil {
		t.Fatal("expected error on version 99, got nil")
	}
}

func TestResolveGuildForChannel(t *testing.T) {
	cat := &Catalog{
		Version: 1,
		Guilds: []Guild{
			{
				ID: "g1",
				Channels: []Channel{
					{ID: "c1", Name: "general"},
					{ID: "c2", Name: "announcements"},
				},
			},
			{
				ID: "g2",
				Channels: []Channel{
					{ID: "c3", Name: "dev"},
					{ID: "c_dup", Name: "duplicate"},
				},
			},
			{
				ID: "", // empty guild id
				Channels: []Channel{
					{ID: "c_noguild", Name: "orphan"},
				},
			},
			{
				ID: "g3",
				Channels: []Channel{
					{ID: "c_dup", Name: "duplicate2"},
				},
			},
		},
	}

	// 1. Success
	gid, err := cat.ResolveGuildForChannel("c1")
	if err != nil || gid != "g1" {
		t.Errorf("expected g1, got gid=%q, err=%v", gid, err)
	}

	gid, err = cat.ResolveGuildForChannel("c3")
	if err != nil || gid != "g2" {
		t.Errorf("expected g2, got gid=%q, err=%v", gid, err)
	}

	// 2. Not found
	_, err = cat.ResolveGuildForChannel("c_missing")
	if err == nil {
		t.Error("expected error for missing channel, got nil")
	}

	// 3. Ambiguous (in g2 and g3)
	_, err = cat.ResolveGuildForChannel("c_dup")
	if err == nil {
		t.Error("expected error for ambiguous channel, got nil")
	}

	// 4. Empty guild ID
	_, err = cat.ResolveGuildForChannel("c_noguild")
	if err == nil {
		t.Error("expected error for channel with empty guild id, got nil")
	}

	// 5. Nil catalog
	var nilCat *Catalog
	_, err = nilCat.ResolveGuildForChannel("c1")
	if err == nil {
		t.Error("expected error for nil catalog, got nil")
	}
}
