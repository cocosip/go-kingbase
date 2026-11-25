package kingbase

import "testing"

// testRewrittenTwoArgs is the profile rewrite of "SELECT ?, ?" shared by the
// placeholder and mode-discovery tests.
const testRewrittenTwoArgs = "SELECT $1, $2"

func TestModeProfiles(t *testing.T) {
	cases := []struct {
		mode    CompatibilityMode
		quotes  string
		bracket bool
		named   bool
		routine bool
	}{
		{ModeAuto, `"`, false, true, false},
		{ModePG, `"`, false, true, false},
		{ModeOracle, `"`, false, false, false},
		{ModeMySQL, "`\"", false, false, false},
		{ModeSQLServer, `"`, true, false, true},
	}
	for _, testCase := range cases {
		profile, hasProfile := testCase.mode.profile()
		if !hasProfile {
			t.Errorf("%s has no profile", testCase.mode)
			continue
		}
		if profile.identifierQuotes != testCase.quotes {
			t.Errorf("%s identifier quotes = %q, want %q", testCase.mode, profile.identifierQuotes, testCase.quotes)
		}
		if profile.bracketQuotes != testCase.bracket {
			t.Errorf("%s bracketQuotes = %t, want %t", testCase.mode, profile.bracketQuotes, testCase.bracket)
		}
		if profile.namedPlaceholders != testCase.named {
			t.Errorf("%s namedPlaceholders = %t, want %t", testCase.mode, profile.namedPlaceholders, testCase.named)
		}
		if profile.routineConvenience != testCase.routine {
			t.Errorf("%s routineConvenience = %t, want %t", testCase.mode, profile.routineConvenience, testCase.routine)
		}
	}
	if _, hasProfile := CompatibilityMode(99).profile(); hasProfile {
		t.Error("unknown mode claims a verified profile")
	}
}

func TestPlanPlaceholdersFollowsProfileQuoting(t *testing.T) {
	backtick := modeProfile{identifierQuotes: "`"}
	query := "SELECT `a?`, ?"
	plan, err := planPlaceholders(backtick, query)
	if err != nil {
		t.Fatalf("backtick plan: %v", err)
	}
	if plan.query != "SELECT `a?`, $1" || plan.args != 1 {
		t.Errorf("backtick plan = %q args %d, want the quoted ? skipped", plan.query, plan.args)
	}
	// The PG profile does not treat backticks as quotes, so both ? are
	// placeholders there: behaviour is profile-driven, not hard-coded.
	pgPlan, err := planPlaceholders(pgModeProfile, query)
	if err != nil {
		t.Fatalf("pg plan: %v", err)
	}
	if pgPlan.args != 2 {
		t.Errorf("pg plan args = %d, want both ? rewritten under the PG profile", pgPlan.args)
	}
	// A bracketed identifier is a quote only when the profile enables it.
	bracket := modeProfile{identifierQuotes: `"`, bracketQuotes: true}
	bracketPlan, err := planPlaceholders(bracket, "SELECT [a?b], ?")
	if err != nil {
		t.Fatalf("bracket plan: %v", err)
	}
	if bracketPlan.query != "SELECT [a?b], $1" || bracketPlan.args != 1 {
		t.Errorf("bracket plan = %q args %d, want the bracketed ? skipped", bracketPlan.query, bracketPlan.args)
	}
}

func TestPlanPlaceholdersKeepsNativeCompatibilityBinds(t *testing.T) {
	// The compatibility modes own :n and @var syntax: those statements pass
	// through with the server-owned parameter count instead of being rewritten.
	for _, mode := range []CompatibilityMode{ModeOracle, ModeMySQL, ModeSQLServer} {
		profile, _ := mode.profile()
		for _, query := range []string{"CALL p(1, :1)", "CALL p(1, :b)", "SELECT @x", "EXEC p @a = 1, @b = @b OUTPUT"} {
			plan, err := planPlaceholders(profile, query)
			if err != nil {
				t.Fatalf("%s %q: %v", mode, query, err)
			}
			if plan.query != query || plan.args != -1 {
				t.Errorf("%s %q -> %q args %d, want native pass-through", mode, query, plan.query, plan.args)
			}
		}
		// Mixing the driver's ? rewrite with a native bind is rejected before
		// sending instead of producing a statement the server cannot parse.
		if _, err := planPlaceholders(profile, "SELECT :1, ?"); err == nil {
			t.Errorf("%s accepted mixed native and ? binds", mode)
		}
	}
	// The PG profile still binds :name/@name as the driver's named arguments
	// (one prefix style per statement).
	pgPlan, err := planPlaceholders(pgModeProfile, "SELECT :a, :b")
	if err != nil {
		t.Fatalf("pg named plan: %v", err)
	}
	if pgPlan.query != testRewrittenTwoArgs || pgPlan.args != 2 || len(pgPlan.names) != 2 {
		t.Errorf("pg named plan = %q args %d names %v", pgPlan.query, pgPlan.args, pgPlan.names)
	}
}
