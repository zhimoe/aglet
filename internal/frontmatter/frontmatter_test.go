package frontmatter

import "testing"

func TestSplit(t *testing.T) {
	cases := []struct {
		name        string
		input       string
		header      string
		body        string
		frontmatter bool
		wantErr     bool
	}{
		{
			name:        "with header",
			input:       "---\nname: test\n---\nBody\n",
			header:      "name: test",
			body:        "Body\n",
			frontmatter: true,
		},
		{
			name:  "no header",
			input: "Body\n",
			body:  "Body\n",
		},
		{
			name:        "bom and crlf",
			input:       "\xef\xbb\xbf---\r\nname: test\r\n---\r\nBody\r\n",
			header:      "name: test",
			body:        "Body\n",
			frontmatter: true,
		},
		{
			name:        "empty header",
			input:       "---\n---\nBody",
			header:      "",
			body:        "Body",
			frontmatter: true,
		},
		{
			name:        "closing at end",
			input:       "---\nname: test\n---",
			header:      "name: test",
			body:        "",
			frontmatter: true,
		},
		{
			name:    "unterminated",
			input:   "---\nname: test\n",
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := Split([]byte(tc.input))
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if doc.HasFrontmatter != tc.frontmatter || doc.Header != tc.header || doc.Body != tc.body {
				t.Fatalf("got %#v", doc)
			}
		})
	}
}
