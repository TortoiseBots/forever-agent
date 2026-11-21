# The agent talks to any bot system only through contracts it owns

forever-agent is used by both teams and must work with the TortoiseBots module, ForeverCore playerbots and later the
merged bot module, so it never depends on one module's internals. Every surface it uses (the add-on protocol,
command replies, status rows) is a versioned contract kept in this repo with golden examples, and a bot module proves
it speaks the contract by passing those examples. The add-on protocol contract is the first one.
