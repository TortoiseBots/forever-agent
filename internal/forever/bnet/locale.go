package bnet

// The client language (#114). The core takes it from the Battle.net logon: Battery::Session::OnLogon stores the
// logon request's locale (bnetserver/Server/Session.cpp:274-276), the realm join writes it to account.locale, and
// the world socket reads it back as the session locale. The realm list ticket's client info carries it as two
// fourCCs (TextLocale, AudioLocale), packed big-endian like every other FourCC in ClientBuildInfo.h.

// Locales are the language names the core knows (common/Common.cpp:20-34) minus "none", the name a client that has
// not chosen one sends.
var Locales = []string{"enUS", "koKR", "frFR", "deDE", "zhCN", "zhTW", "esES", "esMX", "ruRU", "ptBR", "itIT"}

// DefaultLocale is the language the agent claims when Options.Locale is empty.
const DefaultLocale = "enUS"

// ValidLocale reports whether name is one of the core's language names.
func ValidLocale(name string) bool {
	for _, l := range Locales {
		if name == l {
			return true
		}
	}
	return false
}

// resolvedLocale is Options.Locale with the default filled in: an empty option must claim a language, never send an
// empty field.
func (o Options) resolvedLocale() string {
	if o.Locale == "" {
		return DefaultLocale
	}
	return o.Locale
}

// localeFourCC packs a locale name's four characters big-endian, the way ClientBuildInfo.h packs its FourCCs
// ("deDE" -> 0x64654445). A name shorter than four characters is left-padded with zero bytes; the caller passes a
// name from Locales.
func localeFourCC(name string) uint32 {
	var v uint32
	for i := range 4 {
		v <<= 8
		if i < len(name) {
			v |= uint32(name[i])
		}
	}
	return v
}
