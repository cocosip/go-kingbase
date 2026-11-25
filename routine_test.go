package kingbase

import "testing"

func TestRewriteRoutineParsedIdentifier(t *testing.T) {
	profile := modeProfile{
		name:               "sqlserver",
		identifierQuotes:   `"`,
		bracketQuotes:      true,
		routineConvenience: true,
	}
	cases := []struct {
		query string
		want  string
	}{
		{"proc", "EXEC proc"},
		{"dbo.proc", "EXEC dbo.proc"},
		{"dbo . proc", "EXEC dbo . proc"},
		{`[dbo].[my proc]`, `EXEC [dbo].[my proc]`},
		{`[dbo].[a]]b]`, `EXEC [dbo].[a]]b]`},
		{`"dbo"."proc"`, `EXEC "dbo"."proc"`},
		{"  dbo.proc  ", "EXEC dbo.proc"},
		{"#temp", "EXEC #temp"},
		{"@result", "EXEC @result"},
	}
	for _, testCase := range cases {
		if got := profile.rewriteRoutine(testCase.query); got != testCase.want {
			t.Errorf("rewriteRoutine(%q) = %q, want %q", testCase.query, got, testCase.want)
		}
	}
}

func TestRewriteRoutineLeavesUnsafeOrExplicitStatements(t *testing.T) {
	profile := modeProfile{
		name:               "sqlserver",
		identifierQuotes:   `"`,
		bracketQuotes:      true,
		routineConvenience: true,
	}
	cases := []string{
		"",
		"   ",
		"EXEC proc",
		"exec dbo.proc",
		"EXECUTE dbo.proc",
		"CALL proc",
		"SELECT 1",
		"proc @p = 1",
		"proc()",
		"proc;",
		"dbo..proc",
		"dbo.",
		"[unterminated",
		"[]",
		`""`,
		`"unterminated`,
		"dbo.[x] extra",
		"dbo.*",
	}
	for _, query := range cases {
		if got := profile.rewriteRoutine(query); got != query {
			t.Errorf("rewriteRoutine(%q) = %q, want unchanged", query, got)
		}
	}
}

func TestRewriteRoutineRequiresAcceptedProfile(t *testing.T) {
	disabled := modeProfile{name: "sqlserver", identifierQuotes: `"`, bracketQuotes: true}
	for _, query := range []string{"proc", "dbo.proc"} {
		if got := disabled.rewriteRoutine(query); got != query {
			t.Errorf("disabled rewriteRoutine(%q) = %q, want unchanged", query, got)
		}
		// The live V009R001C010 SQLServer-mode listener rejects a bare procedure
		// path, so the verified SQLServer profile enables the rewrite.
		if got := (&conn{mode: ModeSQLServer}).rewriteRoutine(query); got != "EXEC "+query {
			t.Errorf("SQLServer rewriteRoutine(%q) = %q, want EXEC prefix", query, got)
		}
		if got := (&conn{mode: ModePG}).rewriteRoutine(query); got != query {
			t.Errorf("PG rewriteRoutine(%q) = %q, want unchanged", query, got)
		}
	}
}
