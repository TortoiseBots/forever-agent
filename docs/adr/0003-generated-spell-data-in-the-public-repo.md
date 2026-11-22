# Generated spell data is committed to this public repo

Judging a fight needs each class's real spells, cooldowns and levels from the game client's data. We generate small
data files from the client's tables with `tools/foreverspellgen` and commit them here, as the older Turtle tool already did with its spell data,
so every checkout and CI run judges fights the same way. The client's own data files and where they live are never
committed or named.
