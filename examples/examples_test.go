package examples_test

import (
	"io/fs"
	"testing"

	"github.com/blashkar/evalwright/examples"
	"github.com/blashkar/evalwright/internal/assert"
	"github.com/blashkar/evalwright/internal/suite"
)

// Every example must parse and use only real assertion types, so the docs never rot.
func TestExamplesAreValid(t *testing.T) {
	paths, err := fs.Glob(examples.FS, "*.yaml")
	if err != nil || len(paths) == 0 {
		t.Fatalf("no examples found: %v", err)
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			f, err := examples.FS.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			s, err := suite.Parse(f, path)
			if err != nil {
				t.Fatal(err)
			}
			for _, c := range s.Cases {
				if _, err := s.Render(c); err != nil {
					t.Error(err)
				}
				for _, a := range c.Assert {
					if _, err := assert.Build(a.Type, a.Decode); err != nil {
						t.Errorf("case %q: %s: %v", c.Name, a.Type, err)
					}
				}
			}
		})
	}
}
