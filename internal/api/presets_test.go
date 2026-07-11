package api

import "testing"

func TestValidatePresetPayload(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		wantErr bool
	}{
		{"deepsky with valid knobs", `{"mode":"deepsky","params":{"star_reduce":0.1,"denoise_lum":0.4}}`, false},
		{"nebula narrowband, no params", `{"mode":"nebula","palette":"sho","color_calibration":false}`, false},
		{"milkyway top-level only", `{"mode":"milkyway","look":"natural","brightness":"balanced"}`, false},
		{"planetary knobs", `{"mode":"planetary","params":{"sharpen":1.2,"best_percent":30}}`, false},
		{"empty payload", ``, true},
		{"unknown mode", `{"mode":"galaxy"}`, true},
		{"bogus knob is rejected", `{"mode":"deepsky","params":{"not_a_real_knob":1}}`, true},
		{"malformed params", `{"mode":"deepsky","params":123}`, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validatePresetPayload([]byte(c.payload))
			if (err != nil) != c.wantErr {
				t.Fatalf("validatePresetPayload(%s) err=%v, wantErr=%v", c.payload, err, c.wantErr)
			}
		})
	}
}
