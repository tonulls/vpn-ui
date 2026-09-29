package controller

import "testing"

func TestOptionalLimit(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    *int
		wantErr bool
	}{
		{name: "inherit", input: "", want: nil},
		{name: "zero is explicit", input: "0", want: intPointer(0)},
		{name: "positive", input: "12", want: intPointer(12)},
		{name: "negative refused", input: "-1", wantErr: true},
		{name: "not a number refused", input: "fast", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := optionalLimit(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("optionalLimit(%q) error = %v; wantErr %v", tt.input, err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if (got == nil) != (tt.want == nil) {
				t.Fatalf("optionalLimit(%q) = %v; want %v", tt.input, got, tt.want)
			}
			if got != nil && *got != *tt.want {
				t.Errorf("optionalLimit(%q) = %d; want %d", tt.input, *got, *tt.want)
			}
		})
	}
}

func intPointer(value int) *int { return &value }
