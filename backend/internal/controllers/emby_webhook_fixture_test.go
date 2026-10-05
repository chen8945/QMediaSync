package controllers

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"qmediasync/internal/models"
	"qmediasync/internal/requests"
)

func TestEmbyCapturedWebhookFixtures(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("testdata/emby-webhook/provenance.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest []struct {
		Fixture string `json:"fixture"`
		SHA256  string `json:"fixture_sha256"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil || len(manifest) != 12 {
		t.Fatalf("fixture manifest: %v, len=%d", err, len(manifest))
	}
	inputs := make(map[string]models.EmbyWebhookEnvelope)
	for _, entry := range manifest {
		t.Run(entry.Fixture, func(t *testing.T) {
			body, err := os.ReadFile(filepath.Join("testdata/emby-webhook", entry.Fixture))
			if err != nil {
				t.Fatal(err)
			}
			hash := sha256.Sum256(body)
			if hex.EncodeToString(hash[:]) != entry.SHA256 {
				t.Fatal("captured fixture drifted from documented transformation")
			}
			request, err := requests.ParseEmbyWebhook(body)
			if err != nil {
				t.Fatal(err)
			}
			input := request.ToEnvelope()
			if !request.Managed() || input.Blocked || input.ServerID != "fixture-server-1" {
				t.Fatalf("captured supported event rejected: %+v", input)
			}
			if input.ItemPath != request.Item.Path || input.Date != request.Date {
				t.Fatal("captured JSON path or date changed during normalization")
			}
			inputs[entry.Fixture] = input
		})
	}
	if len(inputs) != 12 {
		t.Fatal("some fixtures did not parse")
	}
	newInput, deleted := inputs["official-fast-new.json"], inputs["official-fast-deleted.json"]
	if newInput.ItemID != deleted.ItemID || newInput.SeriesID != deleted.SeriesID || newInput.SeasonID != deleted.SeasonID || len(newInput.Candidates) != 0 {
		t.Fatal("buffered new/deleted topology must survive without invented sources")
	}
	season := inputs["official-season.json"]
	if season.ItemType != "Season" || season.SeriesID == "" || season.IndexNumber == nil || *season.IndexNumber != 1 {
		t.Fatalf("season identity lost: %+v", season)
	}
	if inputs["official-library-root.json"].ItemType != "Folder" {
		t.Fatal("library root event must remain Folder")
	}
	for _, name := range []string{"mik-notification-success.json", "sa-notification-success.json", "sa-notification-failure.json"} {
		input := inputs[name]
		if input.Source != "deep-description-v1" || len(input.Candidates) != 1 {
			t.Fatalf("success/failure bodies use the same supported evidence format: %s %+v", name, input)
		}
	}
	success := inputs["sa-notification-success.json"]
	if success.DeepItemPath == success.ItemPath || !strings.HasSuffix(success.ItemPath, ".strm") {
		t.Fatal("Description directory must not replace JSON physical file")
	}
	if inputs["sa-official-mirror.json"].ItemID != success.ItemID {
		t.Fatal("official/deep pair identity changed")
	}
	for _, name := range []string{"sa-multiple-sources.json", "sa-newline-comma.json"} {
		input := inputs[name]
		if len(input.Candidates) != 0 || !slices.Contains(input.Issues, "ambiguous_multiline_mount_paths") {
			t.Fatalf("ambiguous source block expanded into files: %s %+v", name, input)
		}
	}
	if !strings.Contains(inputs["sa-newline-comma.json"].ItemPath, ", [版本]\n第二行") {
		t.Fatal("real newline filename must remain intact")
	}
	video := inputs["sa-hidden-video.json"]
	if video.ItemType != "Video" || video.ExtraType != "AdditionalPart" || len(video.Candidates) != 1 || video.Candidates[0].PickCode != "fixture-pickcode-1" {
		t.Fatalf("hidden physical part identity lost: %+v", video)
	}
}
