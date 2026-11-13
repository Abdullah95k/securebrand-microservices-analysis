package config

import "testing"

func TestContractKinds(t *testing.T) {
	for k, want := range map[string]bool{
		"image/content": true, "video/attachment": true, "image/profile_picture": true, "audio/audio_cover": true,
		"image": false, "image/": false, "/content": false, "Image/content": false, "image/unspecified": false,
		"unspecified/content": false, "photo/content": false, "image/content/x": false,
		"youtube:image/thumbnail": true, "YouTube:image/thumbnail": false, ":image/thumbnail": false, "youtube:image": false,
	} {
		if got := ValidContractKind(k); got != want {
			t.Errorf("%q: %v, want %v", k, got, want)
		}
	}
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Analysis.Media.ContractKinds) == 0 {
		t.Fatal("no contract media analysed by default")
	}
	cfg.Analysis.Media.ContractKinds = append(cfg.Analysis.Media.ContractKinds, "sticker")
	if err := cfg.Analysis.Validate(); err == nil {
		t.Fatal("a contract kind without a role was accepted")
	}
}
