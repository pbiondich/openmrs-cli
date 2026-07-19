package cli

import "testing"

func TestIsInstancePath(t *testing.T) {
	uuid := "dd8e5b3d-1691-11df-97a5-7038c432aabf"
	cases := []struct {
		path string
		want bool
	}{
		{"patient/" + uuid, true},
		{"/patient/" + uuid, true},
		{"patient/" + uuid + "/", true},
		{"patient", false},
		{"patient/" + uuid + "/encounter", false},
		{"obs", false},
		{"patient/not-a-uuid", false},
	}
	for _, tc := range cases {
		if got := isInstancePath(tc.path); got != tc.want {
			t.Errorf("isInstancePath(%q)=%v want %v", tc.path, got, tc.want)
		}
	}
}
