package heal

// rules is the canonical ordered list. Real entries are added in the
// rule_*.go phase. Defined here so Classify compiles before any rule
// files exist; replaced in Task 11.
var rules = []Rule{}

func registeredRules() []Rule { return rules }
