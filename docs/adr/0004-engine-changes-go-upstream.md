# Engine changes are asked for upstream, never carried in our code

Our repositories hold no changes to ForeverCore itself. When the bots or the agent need something new from the
engine (a script hook, a fix, a helper), we ask for it upstream, as a pull request or a request to the core's author,
and wait for it to ship in a core release. This keeps every server able to run a plain ForeverCore build and keeps
the merged bot module free of private core patches.
