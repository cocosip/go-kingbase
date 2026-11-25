package kingbase

import "testing"

func TestModeProfiles(t *testing.T) {
	for _, mode := range []CompatibilityMode{ModeAuto, ModePG} {
		profile, hasProfile := mode.profile()
		if !hasProfile {
			t.Errorf("%s has no profile", mode)
			continue
		}
		if !profile.quotesIdentifier('"') || profile.quotesIdentifier('`') {
			t.Errorf("%s profile identifier quotes = %q", mode, profile.identifierQuotes)
		}
	}
	for _, mode := range []CompatibilityMode{ModeOracle, ModeMySQL, ModeSQLServer} {
		if _, hasProfile := mode.profile(); hasProfile {
			t.Errorf("%s claims a verified profile without mode evidence", mode)
		}
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
}
