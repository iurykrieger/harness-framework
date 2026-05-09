package heal

// rules is the canonical ordered list. Adding a new rule = creating a
// new lib/heal/rule_<name>.go file with a struct implementing Rule
// and inserting one line into this slice. Order is deterministic and
// matters: more-specific rules go before more-generic ones (heal-hint
// is a fast-path before regex-based rules; missing-env runs first
// because it's the most common).
var rules = []Rule{
	ruleMissingEnv{},
	ruleHealHint{},
	ruleExitCode127{},
	rulePrepareTemplateCopy{},
	ruleStderrPattern{},
}

func registeredRules() []Rule { return rules }
