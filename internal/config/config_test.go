package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestDefaultEnablesClosedTail(t *testing.T) {
	c := Default()
	if !c.ShowClosed {
		t.Error("ShowClosed should default to true (current behavior, non-breaking)")
	}
	if c.ClosedLimit != 15 {
		t.Errorf("ClosedLimit default = %d, want 15", c.ClosedLimit)
	}
}

func TestClosedTailOverridesFromYAML(t *testing.T) {
	c := Default()
	yml := `
showClosed: false
closedLimit: 5
sections:
  - title: My PRs
    filter: "is:open is:pr author:@me"
  - title: Needs Review
    filter: "is:open is:pr review-requested:@me"
    showClosed: true
    closedLimit: 3
`
	if err := yaml.Unmarshal([]byte(yml), &c); err != nil {
		t.Fatal(err)
	}
	if c.ShowClosed {
		t.Error("global showClosed:false should disable the tail")
	}
	if c.ClosedLimit != 5 {
		t.Errorf("global ClosedLimit = %d, want 5", c.ClosedLimit)
	}
	// Section 0 leaves it unset -> inherits (nil pointer).
	if c.Sections[0].ShowClosed != nil {
		t.Error("unset section ShowClosed should be nil (inherit)")
	}
	// Section 1 overrides both.
	if c.Sections[1].ShowClosed == nil || !*c.Sections[1].ShowClosed {
		t.Error("section showClosed:true should override the global off")
	}
	if c.Sections[1].ClosedLimit != 3 {
		t.Errorf("section ClosedLimit = %d, want 3", c.Sections[1].ClosedLimit)
	}
}
