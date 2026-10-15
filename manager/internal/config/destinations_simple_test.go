package config

import "testing"

// Switching a RIST destination to the simple profile drops its passphrase and user instead of refusing the
// switch (the screen sends only the profile).
func TestRISTSimpleDropsSecrets(t *testing.T) {
	cur := Destination{ID: "d-0a1b2c3d", Name: "Studio", Template: "custom", Protocol: ProtoRIST,
		URL: "rist://192.168.1.50:5000", Mode: "caller", LatencyMs: 1000, RISTProfile: "main", Passphrase: "abcdefghijk",
		Username: "enc", Password: "pw.1", VideoCodec: "h264", VideoKbps: 6000, VideoMinKbps: 1000, ABR: true,
		AudioKbps: 128, OutputFPS: "full"}
	if err := cur.Validate(); err != nil {
		t.Fatalf("current: %v", err)
	}
	simple := "simple"
	got, err := DestinationPatch{RISTProfile: &simple}.ApplyTo(cur)
	if err != nil {
		t.Fatalf("switch to simple: %v", err)
	}
	if got.Passphrase != "" || got.Username != "" || got.Password != "" {
		t.Errorf("secrets kept: %+v", got)
	}
	if _, err := (DestinationPatch{RISTProfile: &simple, Passphrase: Optional{Set: true, Value: "abcdefghijk"}}).ApplyTo(cur); err == nil {
		t.Error("a passphrase given with the simple profile was taken")
	}
}
