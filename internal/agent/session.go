package agent

import "github.com/verove-jordan/astronomy/internal/turns"

// Event and Option are the turn-transport types, defined in the leaf package internal/turns so the
// processing engine can also stream turns without an import cycle. They are aliased here so the agent
// loop, tools and prompt keep referring to them unqualified. The live turn hub (turns.Sessions) is
// wired by the API layer, not constructed here.
type (
	Event  = turns.Event
	Option = turns.Option
)
