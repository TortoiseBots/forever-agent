// SRP test vectors for internal/forever/bnet, produced by the server's own SRP6 implementation.
//
// Not built by CI, not part of the Go module: build it by hand against a ForeverCore checkout, e.g.
//
//   CORE=~/ForeverCore
//   g++ -std=c++20 -O2 -I$CORE/src/common -I$CORE/src/common/Cryptography \
//       -I$CORE/src/common/Cryptography/Authentication -I$CORE/src/common/Utilities \
//       -I$CORE/src/common/DataStructures -I$CORE/src/common/Debugging -I$CORE/src/common/Threading \
//       tools/forevervec/srp_vectors.cpp \
//       $CORE/src/common/Cryptography/Authentication/SRP6.cpp \
//       $CORE/src/common/Cryptography/BigNumber.cpp \
//       $CORE/src/common/Cryptography/CryptoRandom.cpp \
//       -lcrypto -o /tmp/srp_vectors
//
// What it does, for a fixed account name, password and salt:
//   1. derives the verifier with the server's CalculateVerifier (the registration path), so the verifier is the row
//      the server would store for that password;
//   2. lets the server's BnetSRP6v2/BnetSRP6v1 produce B for their own random b (B is printed, not derived here);
//   3. computes A, S, M1, M2 as a client does (BigNumber/CryptoHash from the core);
//   4. verifies M1 with the server's VerifyClientEvidence and M2 with CalculateServerEvidence, and asserts that the
//      server's session key is the client's S. A vector only reaches the output if the server code accepted it.
//
// Besides one baseline vector per variant it SEARCHES for the encoding edges of GetBrokenEvidenceVector and
// BN_bn2hex (SRP6.cpp:182-186, BigNumber.cpp:210-216): A, B or S with a leading zero byte, a value whose bit length
// is a multiple of 8 (so the broken encoding gets one extra leading zero), and an M2 whose first byte is zero (which
// the server sends with a dropped leading byte). The search loops over deterministic client private values and
// fresh server b values, and each match still goes through the server's own VerifyClientEvidence/CalculateServerEvidence
// before it is printed. Progress and the number of attempts go to stderr; the JSON goes to stdout.
//
// The printed JSON is committed as internal/forever/bnet/testdata/srp-vectors.json. Note that clientPublicA,
// serverPublicB, sessionS, clientEvidenceM1 and serverEvidenceM2 are all printed with AsHexStr, i.e. leading zero
// bytes dropped, exactly as the server sends the values on the wire.

#include "BigNumber.h"
#include "CryptoHash.h"
#include "SRP6.h"
#include <openssl/evp.h>
#include <algorithm>
#include <array>
#include <chrono>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <iterator>
#include <string>
#include <string_view>
#include <vector>

using namespace Trinity::Crypto;
using namespace Trinity::Crypto::SRP;

// the core's assertion helpers live in Errors.cpp, which needs fmt; the vectors only need the symbols to exist
std::string GetDebugInfo()
{
    return { };
}

namespace Trinity
{
void Assert(char const* file, int line, char const* function, char const* message, std::string) noexcept
{
    std::fprintf(stderr, "assertion failed: %s:%d %s: %s\n", file, line, function, message);
    std::abort();
}

void Assert(char const* file, int line, char const* function, char const* message, std::string, char const* format, ...) noexcept
{
    std::fprintf(stderr, "assertion failed: %s:%d %s: %s (%s)\n", file, line, function, message, format);
    std::abort();
}
}

namespace
{
// fixed inputs of the vectors; changing any of them invalidates the committed file
std::string const Login = "VECTOR@LOCAL";
std::string const Password = "vector-password";
std::string const ClientPrivateHex = "000102030405060708090A0B0C0D0E0F101112131415161718191A1B1C1D1E1F";

bool First = true;

std::string ToHex(std::span<uint8 const> bytes)
{
    static char const digits[] = "0123456789ABCDEF";
    std::string out;
    out.reserve(bytes.size() * 2);
    for (uint8 b : bytes)
    {
        out.push_back(digits[b >> 4]);
        out.push_back(digits[b & 0xF]);
    }
    return out;
}

std::vector<uint8> FromHex(std::string_view hex)
{
    std::vector<uint8> out;
    out.reserve(hex.size() / 2);
    for (std::size_t i = 0; i + 1 < hex.size(); i += 2)
        out.push_back(uint8(std::stoul(std::string(hex.substr(i, 2)), nullptr, 16)));
    return out;
}

// the server's evidence input: big-endian, (bits + 8) >> 3 bytes (one leading zero byte when the bit count is a
// multiple of 8) - SRP6.cpp GetBrokenEvidenceVector
template <typename Hash>
BigNumber Evidence(std::span<BigNumber const*> bns)
{
    Hash hash;
    for (BigNumber const* bn : bns)
    {
        int32 bytes = (bn->GetNumBits() + 8) >> 3;
        std::vector<uint8> v = bn->ToByteVector(bytes, false);
        hash.UpdateData(v);
    }
    hash.Finalize();
    return BigNumber(hash.GetDigest(), false);
}

BigNumber CalculateX(uint32 version, std::string const& srpUsername, std::string const& password, Salt const& salt,
    BigNumber const& N)
{
    if (version == 1)
        return BigNumber(SHA256::GetDigestOf(salt, SHA256::GetDigestOf(srpUsername, ":", password)), true);

    std::string const tmp = srpUsername + ":" + password;
    std::array<uint8, 64> xBytes{};
    PKCS5_PBKDF2_HMAC(tmp.c_str(), tmp.length(), salt.data(), salt.size(), 15000, EVP_sha512(), xBytes.size(),
        xBytes.data());
    BigNumber value(xBytes, false);
    if (xBytes[0] & 0x80)
    {
        std::array<uint8, 65> fix{};
        fix[64] = 1;
        value -= BigNumber(fix); // default little-endian: fix[64] is 2^512
        // (the server passes the array to a BigNumber, so the same default applies)
    }
    return value % (N - 1);
}

// v = g^x mod N with x from the version's password derivation; CheckCredentials below proves it is what the server
// would have stored for this salt and password (SRP6.cpp:193-220, 53-56, 74-77)
std::vector<uint8> DeriveVerifier(BigNumber const& N, BigNumber const& g, uint32 version,
    std::string const& srpUsername, std::string const& password, Salt const& salt)
{
    BigNumber const x = CalculateX(version, srpUsername, password, salt, N);
    return g.ModExp(x, N).ToByteVector();
}

// a deterministic client private for attempt i of the search
BigNumber CounterPrivate(BigNumber const& N, std::size_t attempt)
{
    std::array<uint8, 32> const seed = SHA256::GetDigestOf(std::string("forevervec-a-") + std::to_string(attempt));
    BigNumber a(seed, false);
    a %= N - 1;
    if (a.IsZero())
        a = BigNumber(uint32(1));
    return a;
}

template <std::size_t ModulusSize, typename Impl>
struct CaseValues
{
    BigNumber A;
    BigNumber S;
    BigNumber M1;
    BigNumber M2;
};

template <std::size_t ModulusSize, typename Impl>
CaseValues<ModulusSize, Impl> ComputeCase(Impl const& impl, BigNumber const& a, BigNumber const& x)
{
    BigNumber const& N = impl.GetN();
    BigNumber const& g = impl.Getg();
    BigNumber const& B = impl.B;

    BigNumber const k(SHA256::GetDigestOf(N.ToByteArray<ModulusSize>(false), g.ToByteArray<ModulusSize>(false)), false);
    BigNumber const A = g.ModExp(a, N);
    BigNumber const u(SHA256::GetDigestOf(A.ToByteArray<ModulusSize>(false), B.ToByteArray<ModulusSize>(false)), false);
    BigNumber const base = (B - k * g.ModExp(x, N)) % N;
    BigNumber const S = base.ModExp(a + u * x, N);

    std::array evidenceBns{ &A, &B, &S };
    BigNumber const M1 = Evidence<SHA256>(evidenceBns);
    std::array evidence2Bns{ &A, &M1, &S };
    BigNumber const M2 = Evidence<SHA256>(evidence2Bns);
    return { A, S, M1, M2 };
}

// Emit verifies the case with the server's own code and, only if it accepts, prints one JSON vector.
template <std::size_t ModulusSize, typename Impl>
void Emit(std::string const& name, uint32 version, uint32 iterations, std::string const& password,
    BigNumber const& a, Impl& impl, BigNumber const& x, std::vector<uint8> const& verifier)
{
    BigNumber const& N = impl.GetN();
    BigNumber const& g = impl.Getg();
    CaseValues<ModulusSize, Impl> const c = ComputeCase<ModulusSize, Impl>(impl, a, x);

    Optional<BigNumber> sessionKey = impl.VerifyClientEvidence(c.A, c.M1);
    if (!sessionKey || *sessionKey != c.S)
    {
        std::fprintf(stderr, "%s: the server rejected the client evidence\n", name.c_str());
        std::exit(1);
    }
    BigNumber const serverM2 = impl.CalculateServerEvidence(c.A, c.M1, *sessionKey);
    if (serverM2 != c.M2)
    {
        std::fprintf(stderr, "%s: server M2 != client M2\n", name.c_str());
        std::exit(1);
    }

    std::printf(First ? "  {\n" : ",\n  {\n");
    First = false;
    std::printf("    \"name\": \"%s\",\n", name.c_str());
    std::printf("    \"version\": %u,\n", version);
    std::printf("    \"iterations\": %u,\n", iterations);
    std::printf("    \"login\": \"%s\",\n", Login.c_str());
    std::printf("    \"password\": \"%s\",\n", password.c_str());
    std::string const srpUsername = ToHex(SHA256::GetDigestOf(Login));
    std::printf("    \"srpUsername\": \"%s\",\n", srpUsername.c_str());
    std::printf("    \"modulus\": \"%s\",\n", N.AsHexStr().c_str());
    std::printf("    \"generator\": \"%s\",\n", g.AsHexStr().c_str());
    Salt const salt = impl.s;
    std::printf("    \"salt\": \"%s\",\n", ToHex(salt).c_str());
    std::printf("    \"verifier\": \"%s\",\n", ToHex(verifier).c_str());
    std::printf("    \"clientPrivate\": \"%s\",\n", a.AsHexStr().c_str());
    std::printf("    \"clientPublicA\": \"%s\",\n", c.A.AsHexStr().c_str());
    std::printf("    \"serverPublicB\": \"%s\",\n", impl.B.AsHexStr().c_str());
    std::printf("    \"sessionS\": \"%s\",\n", c.S.AsHexStr().c_str());
    std::printf("    \"clientEvidenceM1\": \"%s\",\n", c.M1.AsHexStr().c_str());
    std::printf("    \"serverEvidenceM2\": \"%s\"\n", c.M2.AsHexStr().c_str());
    std::printf("  }");
}

bool HasLeadingZeroByte(BigNumber const& v, std::size_t modulusBytes)
{
    return v.GetNumBytes() > 0 && std::size_t(v.GetNumBytes()) < modulusBytes;
}

bool BitLengthMultipleOf8(BigNumber const& v)
{
    return v.GetNumBits() > 0 && (v.GetNumBits() % 8) == 0;
}

struct Edge
{
    char const* suffix;
    char const* description;
};

// SearchEdges looks for the encoding edges the plain vector does not cover. Every match is still accepted by the
// server's own VerifyClientEvidence/CalculateServerEvidence in Emit.
template <std::size_t ModulusSize, typename Impl>
void SearchEdges(std::string const& prefix, uint32 version, uint32 iterations, std::string const& password,
    std::string const& srpUsername, Salt const& salt, std::vector<uint8> const& verifier)
{
    static constexpr Edge Edges[] = {
        { "a-leading-zero", "A with a leading zero byte in the modulus-size encoding" },
        { "b-leading-zero", "B with a leading zero byte in the modulus-size encoding" },
        { "s-leading-zero", "S with a leading zero byte in the modulus-size encoding" },
        { "bit-length-multiple-of-8", "an evidence value whose bit length is a multiple of 8" },
        { "m2-leading-zero", "M2 with a leading zero byte (dropped by BN_bn2hex)" },
    };
    static constexpr std::size_t MaxAttempts = 200000;

    bool found[std::size(Edges)] = { };
    std::size_t foundCount = 0;
    std::size_t attempts = 0;
    auto const started = std::chrono::steady_clock::now();

    for (; attempts < MaxAttempts && foundCount < std::size(Edges); ++attempts)
    {
        BigNumber const a = CounterPrivate(Impl::N, attempts);
        Impl impl(srpUsername, salt, verifier);
        BigNumber const x = CalculateX(version, srpUsername, password, salt, Impl::N);
        CaseValues<ModulusSize, Impl> const c = ComputeCase<ModulusSize, Impl>(impl, a, x);

        std::size_t edge = std::size(Edges);
        for (std::size_t i = 0; i < std::size(Edges); ++i)
        {
            if (found[i])
                continue;
            bool match = false;
            switch (i)
            {
                case 0: match = HasLeadingZeroByte(c.A, ModulusSize); break;
                case 1: match = HasLeadingZeroByte(impl.B, ModulusSize); break;
                case 2: match = HasLeadingZeroByte(c.S, ModulusSize); break;
                case 3: match = BitLengthMultipleOf8(c.A) || BitLengthMultipleOf8(impl.B) || BitLengthMultipleOf8(c.S); break;
                case 4: match = c.M2.GetNumBytes() > 0 && c.M2.GetNumBytes() < 32; break;
                default: break;
            }
            if (match)
            {
                edge = i;
                break;
            }
        }
        if (edge == std::size(Edges))
            continue;

        found[edge] = true;
        ++foundCount;
        // Emit marks this server object used, so at most one edge is taken per attempt
        Emit<ModulusSize, Impl>(prefix + "-" + Edges[edge].suffix, version, iterations, password, a, impl, x, verifier);
    }

    auto const seconds = std::chrono::duration<double>(std::chrono::steady_clock::now() - started).count();
    std::fprintf(stderr, "%s: %zu attempts in %.2f s\n", prefix.c_str(), attempts, seconds);
    for (std::size_t i = 0; i < std::size(Edges); ++i)
        if (!found[i])
            std::fprintf(stderr, "%s: NOT FOUND in %zu attempts: %s\n", prefix.c_str(), attempts, Edges[i].description);
}

// a fixed salt keeps the file reproducible in everything the server does not randomise (B is random: b is)
Salt FixedSalt()
{
    Salt salt{};
    for (std::size_t i = 0; i < salt.size(); ++i)
        salt[i] = uint8(i * 7 + 3);
    return salt;
}
}

int main()
{
    Salt const salt = FixedSalt();
    std::string const srpUsername = ToHex(SHA256::GetDigestOf(Login));

    std::printf("[\n");

    {
        std::string const passwordV1 = [] {
            std::string out = Password;
            for (char& c : out)
                if (c >= 'a' && c <= 'z')
                    c = char(c - ('a' - 'A'));
            return out;
        }();

        std::vector<uint8> const verifier2 = DeriveVerifier(BnetSRP6v2<SHA256>::N, BnetSRP6v2<SHA256>::g, 2,
            srpUsername, Password, salt);
        {
            BnetSRP6v2<SHA256> server(srpUsername, salt, verifier2);
            if (!server.CheckCredentials(srpUsername, Password))
            {
                std::fprintf(stderr, "v2: the server does not accept the verifier we just derived\n");
                return 1;
            }
            BigNumber const a(FromHex(ClientPrivateHex), false);
            BigNumber const x = CalculateX(2, srpUsername, Password, salt, server.GetN());
            Emit<256, BnetSRP6v2<SHA256>>("bnet-srp6-v2-sha256", 2, 15000, Password, a, server, x, verifier2);
        }
        SearchEdges<256, BnetSRP6v2<SHA256>>("bnet-srp6-v2-sha256", 2, 15000, Password, srpUsername, salt, verifier2);

        std::vector<uint8> const verifier1 = DeriveVerifier(BnetSRP6v1<SHA256>::N, BnetSRP6v1<SHA256>::g, 1,
            srpUsername, passwordV1, salt);
        {
            BnetSRP6v1<SHA256> server(srpUsername, salt, verifier1);
            if (!server.CheckCredentials(srpUsername, passwordV1))
            {
                std::fprintf(stderr, "v1: the server does not accept the verifier we just derived\n");
                return 1;
            }
            BigNumber const a(FromHex(ClientPrivateHex), false);
            BigNumber const x = CalculateX(1, srpUsername, passwordV1, salt, server.GetN());
            Emit<128, BnetSRP6v1<SHA256>>("bnet-srp6-v1-sha256", 1, 1, passwordV1, a, server, x, verifier1);
        }
        SearchEdges<128, BnetSRP6v1<SHA256>>("bnet-srp6-v1-sha256", 1, 1, passwordV1, srpUsername, salt, verifier1);
    }

    std::printf("\n]\n");
    return 0;
}
