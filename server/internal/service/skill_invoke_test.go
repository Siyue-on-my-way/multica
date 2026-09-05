package service

import "testing"

func TestSkillNameInText(t *testing.T) {
	cases := []struct {
		name string
		text string
		want bool
	}{
		// Bare name, @-prefixed, and mid-sentence — the invocation contract.
		{"bare", "please run my-skill", true},
		{"at-prefixed", "@my-skill 处理这个", true},
		{"mid-sentence", "use the My-Skill skill for this", true},
		{"punctuated", "先执行 my-skill，然后报告", true},
		{"start", "my-skill: do the thing", true},
		{"end", "try my-skill", true},
		{"code-span", "invoke `my-skill` via skill run", true},
		// Near-miss names must not invoke.
		{"plural-suffix", "check the my-skills folder", false},
		{"prefixed", "run fix-my-skill now", false},
		{"longer-name", "my-skill-v2 is a different skill", false},
		{"underscore-different", "my_skill is a different name", false},
		{"partial", "myskil", false},
		{"empty-text", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SkillNameInText(tc.text, "my-skill"); got != tc.want {
				t.Fatalf("SkillNameInText(%q, %q) = %v, want %v", tc.text, "my-skill", got, tc.want)
			}
		})
	}
}

func TestSkillNameInTextOtherNames(t *testing.T) {
	if !SkillNameInText("使用 sql-explorer 查询", "sql-explorer") {
		t.Fatal("expected CJK-adjacent name to match")
	}
	if SkillNameInText("nothing relevant here", "sql-explorer") {
		t.Fatal("expected no match for absent name")
	}
	if SkillNameInText("run the empty-name test", "") {
		t.Fatal("empty skill name must never match")
	}
}
