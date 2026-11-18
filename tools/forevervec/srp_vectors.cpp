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
// What it does, for a fixed account name, password, salt and client private "a":
//   1. derives the verifier with the server's CalculateVerifier (the registration path), so the verifier is the row
//      the server would store for that password;
//   2. lets the server's BnetSRP6v2/BnetSRP6v1 produce B (b is random, so B is printed, not derived here);
//   3. computes A, S, M1, M2 as a client does (BigNumber/CryptoHash from the core);
//   4. verifies M1 with the server's VerifyClientEvidence and M2 with CalculateServerEvidence, and asserts that the
//      server's session key is the client's S. A vector only reaches the output if the server code accepted it.
//
// The printed JSON is committed as internal/forever/bnet/testdata/srp-vectors.json.

#include "BigNumber.h"
#include "CryptoHash.h"
#include "SRP6.h"
#include <openssl/evp.h>
#include <array>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <string>
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

// v = g^x mod N with x from the version's password derivation; CheckCredentials below proves it is what the server would
// have stored for this salt and password (SRP6.cpp:193-220, 53-56, 74-77)
std::vector<uint8> DeriveVerifier(BigNumber const& N, BigNumber const& g, uint32 version, std::string const& srpUsername,
    std::string const& password, Salt const& salt)
{
    BigNumber x;
    if (version == 1)
    {
        x = BigNumber(SHA256::GetDigestOf(salt, SHA256::GetDigestOf(srpUsername, ":", password)), true);
    }
    else
    {
        std::string const tmp = srpUsername + ":" + password;
        std::array<uint8, 64> xBytes{};
        PKCS5_PBKDF2_HMAC(tmp.c_str(), tmp.length(), salt.data(), salt.size(), 15000, EVP_sha512(), xBytes.size(), xBytes.data());
        x = BigNumber(xBytes, false);
        if (xBytes[0] & 0x80)
        {
            std::array<uint8, 65> fix{};
            fix[64] = 1;
            x -= BigNumber(fix); // default little-endian: fix[64] is 2^512
            // (the server passes the array to a BigNumber, so the same default applies)
        }
        x %= N - 1;
    }

    return g.ModExp(x, N).ToByteVector();
}

template <typename Hash, std::size_t ModulusSize, typename Impl>
struct Vector
{
    static void Run(char const* name, uint32 version, uint32 iterations, std::string const& password, Impl& impl)
    {
        BigNumber const& N = impl.GetN();
        BigNumber const& g = impl.Getg();
        std::string const srpUsername = ToHex(SHA256::GetDigestOf(Login));

        Salt const salt = impl.s;
        std::string saltHex = ToHex(salt);

        std::vector<uint8> verifier = DeriveVerifier(N, g, version, srpUsername, password, salt);
        if (!impl.CheckCredentials(srpUsername, password))
        {
            std::fprintf(stderr, "%s: the server does not accept the verifier we just derived\n", name);
            std::exit(1);
        }

        BigNumber const x = version == 1
            ? BigNumber(SHA256::GetDigestOf(salt, SHA256::GetDigestOf(srpUsername, ":", password)), true)
            : [&]
            {
                std::string const tmp = srpUsername + ":" + password;
                std::array<uint8, 64> xBytes{};
                PKCS5_PBKDF2_HMAC(tmp.c_str(), tmp.length(), salt.data(), salt.size(), 15000, EVP_sha512(), xBytes.size(), xBytes.data());
                BigNumber value(xBytes, false);
                if (xBytes[0] & 0x80)
                {
                    std::array<uint8, 65> fix{};
                    fix[64] = 1;
                    value -= BigNumber(fix);
                }
                return value % (N - 1);
            }();
        BigNumber const k(SHA256::GetDigestOf(N.ToByteArray<ModulusSize>(false), g.ToByteArray<ModulusSize>(false)), false);
        BigNumber const a(FromHex(ClientPrivateHex), false);
        BigNumber const A = g.ModExp(a, N);
        BigNumber const& B = impl.B;

        BigNumber const u(SHA256::GetDigestOf(A.ToByteArray<ModulusSize>(false), B.ToByteArray<ModulusSize>(false)), false);
        BigNumber const base = (B - k * g.ModExp(x, N)) % N;
        BigNumber const S = base.ModExp(a + u * x, N);

        std::array evidenceBns{ &A, &B, &S };
        BigNumber const M1 = Evidence<Hash>(evidenceBns);
        std::array evidence2Bns{ &A, &M1, &S };
        BigNumber const M2 = Evidence<Hash>(evidence2Bns);

        // the server side: it only accepts if its own S and M1 match, and its M2 must be ours
        Optional<BigNumber> sessionKey = impl.VerifyClientEvidence(A, M1);
        if (!sessionKey || *sessionKey != S)
        {
            std::fprintf(stderr, "%s: the server rejected the client evidence\n", name);
            std::exit(1);
        }
        BigNumber const serverM2 = impl.CalculateServerEvidence(A, M1, *sessionKey);
        if (serverM2 != M2)
        {
            std::fprintf(stderr, "%s: server M2 != client M2\n", name);
            std::exit(1);
        }

        std::printf("  {\n");
        std::printf("    \"name\": \"%s\",\n", name);
        std::printf("    \"version\": %u,\n", version);
        std::printf("    \"iterations\": %u,\n", iterations);
        std::printf("    \"login\": \"%s\",\n", Login.c_str());
        std::printf("    \"password\": \"%s\",\n", password.c_str());
        std::printf("    \"srpUsername\": \"%s\",\n", srpUsername.c_str());
        std::printf("    \"modulus\": \"%s\",\n", N.AsHexStr().c_str());
        std::printf("    \"generator\": \"%s\",\n", g.AsHexStr().c_str());
        std::printf("    \"salt\": \"%s\",\n", saltHex.c_str());
        std::printf("    \"verifier\": \"%s\",\n", ToHex(verifier).c_str());
        std::printf("    \"clientPrivate\": \"%s\",\n", ClientPrivateHex.c_str());
        std::printf("    \"clientPublicA\": \"%s\",\n", A.AsHexStr().c_str());
        std::printf("    \"serverPublicB\": \"%s\",\n", B.AsHexStr().c_str());
        std::printf("    \"sessionS\": \"%s\",\n", S.AsHexStr().c_str());
        std::printf("    \"clientEvidenceM1\": \"%s\",\n", M1.AsHexStr().c_str());
        std::printf("    \"serverEvidenceM2\": \"%s\"\n", M2.AsHexStr().c_str());
        std::printf("  }");
    }
};


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
        std::vector<uint8> verifier = DeriveVerifier(BnetSRP6v2<SHA256>::N, BnetSRP6v2<SHA256>::g, 2, srpUsername, Password, salt);
        BnetSRP6v2<SHA256> server(srpUsername, salt, verifier);
        Vector<SHA256, 256, BnetSRP6v2<SHA256>>::Run("bnet-srp6-v2-sha256", 2, 15000, Password, server);
        std::printf(",\n");
    }

    {
        // version 1: the client and the server's password check uppercase the password (LoginRESTService.cpp:346-349)
        std::string passwordV1 = Password;
        for (char& c : passwordV1)
            if (c >= 'a' && c <= 'z')
                c = char(c - ('a' - 'A'));
        std::vector<uint8> verifier = DeriveVerifier(BnetSRP6v1<SHA256>::N, BnetSRP6v1<SHA256>::g, 1, srpUsername, passwordV1, salt);
        BnetSRP6v1<SHA256> server(srpUsername, salt, verifier);
        Vector<SHA256, 128, BnetSRP6v1<SHA256>>::Run("bnet-srp6-v1-sha256", 1, 1, passwordV1, server);
        std::printf("\n");
    }

    std::printf("]\n");
    return 0;
}
