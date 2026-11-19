package wire

// The character-screen opcodes of this round: create, delete and the name-availability check. The numbers are the
// core's OpcodeClient/OpcodeServer values (Opcodes.h:192, 313, 352, 1357, 1422, 1437); the wire numbers come from
// OpcodeTable like every other opcode, so they differ per client and realm build.
const (
	CMSGCreateCharacter                uint32 = 0x430070 // Opcodes.h:313
	CMSGCheckCharacterNameAvailability uint32 = 0x430071 // Opcodes.h:237
	CMSGCharDelete                     uint32 = 0x4300CB // Opcodes.h:192

	SMSGCreateChar                           uint32 = 0x4501AC // Opcodes.h:1422
	SMSGDeleteChar                           uint32 = 0x4501AD // Opcodes.h:1437
	SMSGCheckCharacterNameAvailabilityResult uint32 = 0x45001B // Opcodes.h:1357
)
