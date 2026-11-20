# cmdctl round C2: the control package

`internal/forever/cmdctl` is the controller over a session's own `.tb` command channel: bind and unbind a party,
read the commander's binding card, send orders, roles and run verbs, and the capability probe for module packages.
It sits on the typed client in `tbcmd` (round C1 added `.tb commander status`).

## Ownership: check, act, check

The session character is the commander. Every mutation names a lead or bot that must be in the character's own
bindings. The module is the authority for GM commands, so the client cannot make the read and the line one atomic
step. What it does instead:

- It reads `.tb commander status <self>` immediately before each mutation (the run guard's read is the ownership read
  for orders and roles) and sends nothing when the target is not there (`NotOwnedError`).
- It reads again right after the line. A target that is gone at that read gives `NotOwnedError` with `Sent` set; its
  message says the line already went through. A bind, an order, a role or a run verb that was in flight when another
  session changed the binding is therefore reported, not hidden. It is not undone.
- Unbind reads before (the lead must be this character's) and not after: the lead is expected to be gone.
- Only cards whose `cmd=` is the session character count; a foreign card in the reply is ignored.
- Bind reads the whole table (`.tb commander status`) first and refuses a lead that is the character itself, a
  commander, a real player's party, or a bot another commander leads or holds (`BindRefusedError`). A bot nobody
  commands, or one already bound to this character, is allowed.

The window between the last read and the line stays open for the length of one round trip. Closing it needs a
commander-scoped mutation in the module.

## Answers

- An answer with nothing queued (every line refused) is a `RefusedError` naming each bot's code; the `Result` still
  carries the per-bot lines.
- A role goes through the run guard like an order: refused while a run runs, unless `PauseRun` is set.
- The capability probe caches only explicit answers: the expected reply (package in), or `unknown-verb`,
  `unknown-command` or `not-available` (package out). Any other module error and every transport failure is returned
  and not cached.

## Tests

One test per finding in `cmdctl_test.go` (`TestOwnershipLostBetweenReadAndLineIsReported`,
`TestRunLeadLostAfterTheLineIsReported`, `TestOrderedBotLostAfterTheLineIsReported`,
`TestUnbindRefusesLeadOutsideTheBindings`, `TestForeignCardsAreIgnored`, `TestAllRefusedIsAnError`,
`TestProbeOtherModuleErrorsAreNotCached`, `TestBindRefusesLeadsItMayNotBind`, `TestRoleHeldByARun`).
