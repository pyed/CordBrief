package state

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func TestV305MigrationPreservesEverySetting(t *testing.T) {
	s := NewStore(t.TempDir())
	want := DefaultConfig()
	want.Version = 3
	want.Channels = []ChannelConfig{{ID: "123", Name: "custom"}}
	want.Schedule = ScheduleConfig{Enabled: true, Time: "19:45"}
	want.Timezone = "Asia/Riyadh"
	want.LLM = LLMConfig{BaseURL: "http://localhost:8080/v1", Model: "custom-model"}
	want.Brief = &BriefConfig{Prompt: "Keep this prompt"}
	want.DCECooldownSeconds = 0
	raw, _ := json.Marshal(want)
	if err := os.WriteFile(s.ConfigPath(), raw, 0600); err != nil {
		t.Fatal(err)
	}
	want.Version = CurrentConfigVersion
	for range 2 {
		got, err := s.LoadConfig()
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("migration lost settings: %v, %+v", err, got)
		}
	}
	want.HiddenChannels = []ChannelConfig{{ID: "456", Name: "hidden"}}
	want.Fallback = &FallbackConfig{LLMConfig: LLMConfig{BaseURL: "https://other.example/v1", Model: "fallback"}}
	if err := s.SaveConfig(want); err != nil {
		t.Fatal(err)
	}
	got, err := NewStore(s.DataDir()).LoadConfig()
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatal("new fields lost on restart", err)
	}
}
