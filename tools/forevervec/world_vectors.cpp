// World-socket test vectors for internal/forever/worldconn, produced by the server's own code.
//
// Not built by CI, not part of the Go module: build it by hand against a ForeverCore checkout, e.g.
//
//   CORE=~/ForeverCore
//   g++ -std=c++20 -O2 -I$CORE/src/common -I$CORE/src/common/Cryptography \
//       -I$CORE/src/common/Cryptography/Authentication -I$CORE/src/common/Utilities \
//       -I$CORE/src/common/DataStructures -I$CORE/src/common/Debugging -I$CORE/src/common/Threading \
//       tools/forevervec/world_vectors.cpp \
//       $CORE/src/common/Cryptography/AES.cpp \
//       $CORE/src/common/Cryptography/Authentication/WorldPacketCrypt.cpp \
//       -lcrypto -lz -o /tmp/world_vectors
//   /tmp/world_vectors > internal/forever/worldconn/testdata/world-vectors.json
//
// What it produces, all from the core's own sources (HMAC.h, CryptoHash.h, SessionKeyGenerator.h, AES.cpp,
// WorldPacketCrypt.cpp) or from zlib called exactly as WorldSocket::CompressPacket calls it:
//
//   sessionKey            WorldSocket.cpp:796-806: SHA512(keyData) -> HMAC-SHA512(serverChallenge,
//                         localChallenge, SessionKeySeed) -> SessionKeyGenerator<SHA512> 40 bytes
//   encryptKey            WorldSocket.cpp:808-816: HMAC-SHA512(sessionKey, localChallenge, serverChallenge,
//                         EncryptionKeySeed) first 32 bytes
//   authDigest            WorldSocket.cpp:775-791 with a synthetic 16-byte build key (the live auth database has no
//                         build_auth_key row for 70707/70938, so the server skips this comparison)
//   continuedDigest       WorldSocket.cpp:1040-1047: HMAC-SHA512(sessionKey, LE64(key), localChallenge,
//                         instanceChallenge, ContinuedSessionSeed) first 24 bytes
//   gcm[]                 AES-256-GCM frames, 12-byte tag: server -> client through WorldPacketCrypt::EncryptSend
//                         (the real server path) and client -> server through Trinity::Crypto::AES, which is the
//                         same primitive WorldPacketCrypt uses for the client direction
//   firstEncryptedFrames  the frame each direction really sends first: two plain frames go through
//                         WorldPacketCrypt while it is not initialized (the counters advance, the bytes do not), the
//                         key is installed, and the next frame is produced at counter 2
//   compression[]         two packets in ONE raw-deflate stream (deflateInit2(level 1, -15, memLevel 8,
//                         Z_DEFAULT_STRATEGY)), opcode first, per-packet Z_SYNC_FLUSH, and the two adler32 values
//                         with the server's seed 0x9827D8F1

#include "AES.h"
#include "CryptoHash.h"
#include "HMAC.h"
#include "SessionKeyGenerator.h"
#include "WorldPacketCrypt.h"
#include <zlib.h>
#include <array>
#include <cstdio>
#include <cstring>
#include <string>
#include <vector>

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
std::array<uint8, 32> const SessionKeySeed = { 0xE8, 0x1E, 0x8B, 0x59, 0x27, 0x62, 0x1E, 0xAA, 0x86, 0x15, 0x18, 0xEA, 0xC0, 0xBF, 0x66, 0x8C,
    0x6D, 0xBF, 0x83, 0x93, 0xBC, 0xAA, 0x80, 0x52, 0x5B, 0x1E, 0xDC, 0x23, 0xA0, 0x12, 0xB7, 0x50 };
std::array<uint8, 32> const EncryptionKeySeed = { 0x71, 0xC9, 0xED, 0x5A, 0xA7, 0x0E, 0x4D, 0xFF, 0x4C, 0x36, 0xA6, 0x5A, 0x3E, 0x46, 0x8A, 0x4A,
    0x5D, 0xA1, 0x48, 0xC8, 0x30, 0x47, 0x4A, 0xDE, 0xF6, 0x0D, 0x6C, 0xBE, 0x6F, 0xE4, 0x55, 0x73 };
std::array<uint8, 32> const AuthCheckSeed = { 0xDE, 0x3A, 0x2A, 0x8E, 0x6B, 0x89, 0x52, 0x66, 0x88, 0x9D, 0x7E, 0x7A, 0x77, 0x1D, 0x5D, 0x1F,
    0x4E, 0xD9, 0x0C, 0x23, 0x9B, 0xCD, 0x0E, 0xDC, 0xD2, 0xE8, 0x04, 0x3A, 0x68, 0x64, 0xC7, 0xB0 };
std::array<uint8, 32> const ContinuedSessionSeed = { 0x56, 0x5C, 0x61, 0x9C, 0x48, 0x3A, 0x52, 0x1F, 0x61, 0x5D, 0x05, 0x49, 0xB2, 0x9A, 0x39, 0xBF,
    0x4B, 0x97, 0xB0, 0x1B, 0xF9, 0x6C, 0xDE, 0xD6, 0x80, 0x1D, 0xAB, 0x26, 0x02, 0xA9, 0x9B, 0x9D };
std::array<uint8, 32> const EnableEncryptionSeed = { 0x66, 0xBE, 0x29, 0x79, 0xEF, 0xF2, 0xD5, 0xB5, 0x61, 0x53, 0xF6, 0x5F, 0x45, 0xAE, 0x81, 0xCB,
    0x32, 0xEC, 0x94, 0xEC, 0x75, 0xB3, 0x5F, 0x44, 0x6A, 0x63, 0x43, 0x67, 0x17, 0x20, 0x44, 0x34 };
constexpr uint32 CompressionAdlerSeed = 0x9827D8F1;

using Bytes = std::vector<uint8>;

std::string ToHex(uint8 const* p, size_t n)
{
    static char const digits[] = "0123456789ABCDEF";
    std::string out;
    out.reserve(n * 2);
    for (size_t i = 0; i < n; ++i)
    {
        out.push_back(digits[p[i] >> 4]);
        out.push_back(digits[p[i] & 0xF]);
    }
    return out;
}

std::string ToHex(Bytes const& b) { return ToHex(b.data(), b.size()); }

Bytes Sequence(uint8 first, size_t n)
{
    Bytes out(n);
    for (size_t i = 0; i < n; ++i)
        out[i] = uint8(first + i);
    return out;
}

std::array<uint8, 12> MakeIV(uint64 counter, uint32 magic)
{
    std::array<uint8, 12> iv;
    std::memcpy(iv.data(), &counter, sizeof(counter));
    std::memcpy(iv.data() + 8, &magic, sizeof(magic));
    return iv;
}

std::string JsonString(std::string const& s) { return "\"" + s + "\""; }

bool FirstGCM = true;

void PrintGCM(char const* direction, uint32 counter, Bytes const& plaintext, Bytes const& ciphertext, uint8 const* tag)
{
    std::printf("%s      { \"direction\": %s, \"counter\": %u, \"plaintext\": %s, \"tag\": %s, \"ciphertext\": %s }",
        FirstGCM ? "" : ",\n", JsonString(direction).c_str(), counter, JsonString(ToHex(plaintext)).c_str(),
        JsonString(ToHex(tag, 12)).c_str(), JsonString(ToHex(ciphertext)).c_str());
    FirstGCM = false;
}
}

int main()
{
    using namespace Trinity::Crypto;
    // fixed inputs; changing them invalidates the committed JSON
    Bytes clientSecret = Sequence(0x00, 32);
    Bytes joinSecret = Sequence(0x20, 32);
    Bytes keyData = clientSecret;
    keyData.insert(keyData.end(), joinSecret.begin(), joinSecret.end());
    Bytes serverChallenge = Sequence(0x40, 32);
    Bytes localChallenge = Sequence(0x60, 32);
    Bytes instanceChallenge = Sequence(0x80, 32);
    Bytes buildAuthKey = Sequence(0xA0, 16);
    uint64 const continuedKey = 0x1122334455667788ULL;

    // ---- keys, exactly WorldSocket::HandleAuthSessionCallback
    SHA512 keyDataHash;
    keyDataHash.UpdateData(keyData.data(), keyData.size());
    keyDataHash.Finalize();
    HMAC_SHA512 sessionKeyHmac(keyDataHash.GetDigest());
    sessionKeyHmac.UpdateData(serverChallenge.data(), serverChallenge.size());
    sessionKeyHmac.UpdateData(localChallenge.data(), localChallenge.size());
    sessionKeyHmac.UpdateData(SessionKeySeed);
    sessionKeyHmac.Finalize();
    std::array<uint8, 40> sessionKey = { };
    {
        SessionKeyGenerator<SHA512> gen(sessionKeyHmac.GetDigest());
        gen.Generate(sessionKey.data(), uint32(sessionKey.size()));
    }
    HMAC_SHA512 encryptKeyHmac{ std::span<uint8 const>(sessionKey) };
    encryptKeyHmac.UpdateData(localChallenge.data(), localChallenge.size());
    encryptKeyHmac.UpdateData(serverChallenge.data(), serverChallenge.size());
    encryptKeyHmac.UpdateData(EncryptionKeySeed);
    encryptKeyHmac.Finalize();
    std::array<uint8, 32> encryptKey = { };
    std::memcpy(encryptKey.data(), encryptKeyHmac.GetDigest().data(), encryptKey.size());

    // ---- CMSG_AUTH_SESSION digest (skipped by the live server; the formula is what a key would be checked with)
    SHA512 digestKeyHash;
    digestKeyHash.UpdateData(keyData.data(), keyData.size());
    digestKeyHash.UpdateData(buildAuthKey.data(), buildAuthKey.size());
    digestKeyHash.Finalize();
    HMAC_SHA512 authDigestHmac(digestKeyHash.GetDigest());
    authDigestHmac.UpdateData(localChallenge.data(), localChallenge.size());
    authDigestHmac.UpdateData(serverChallenge.data(), serverChallenge.size());
    authDigestHmac.UpdateData(AuthCheckSeed);
    authDigestHmac.Finalize();
    std::array<uint8, 24> authDigest = { };
    std::memcpy(authDigest.data(), authDigestHmac.GetDigest().data(), authDigest.size());

    // ---- CMSG_AUTH_CONTINUED_SESSION digest
    std::array<uint8, 8> keyBytes;
    std::memcpy(keyBytes.data(), &continuedKey, sizeof(continuedKey));
    HMAC_SHA512 continuedHmac{ std::span<uint8 const>(sessionKey) };
    continuedHmac.UpdateData(keyBytes.data(), keyBytes.size());
    continuedHmac.UpdateData(localChallenge.data(), localChallenge.size());
    continuedHmac.UpdateData(instanceChallenge.data(), instanceChallenge.size());
    continuedHmac.UpdateData(ContinuedSessionSeed);
    continuedHmac.Finalize();
    std::array<uint8, 24> continuedDigest = { };
    std::memcpy(continuedDigest.data(), continuedHmac.GetDigest().data(), continuedDigest.size());

    // ---- SMSG_ENTER_ENCRYPTED_MODE: the 64 bytes the server signs (Ed25519 is not verified by this client)
    HMAC_SHA512 enableHmac{ std::span<uint8 const>(encryptKey) };
    uint8 enabled = 1;
    enableHmac.UpdateData(&enabled, 1);
    enableHmac.UpdateData(EnableEncryptionSeed);
    enableHmac.Finalize();
    std::array<uint8, 64> enableInput = { };
    std::memcpy(enableInput.data(), enableHmac.GetDigest().data(), enableInput.size());

    std::printf("{\n");
    std::printf("  \"note\": \"produced by tools/forevervec/world_vectors.cpp against ForeverCore 6517af05\",\n");
    std::printf("  \"keyData\": %s,\n", JsonString(ToHex(keyData)).c_str());
    std::printf("  \"serverChallenge\": %s,\n", JsonString(ToHex(serverChallenge)).c_str());
    std::printf("  \"localChallenge\": %s,\n", JsonString(ToHex(localChallenge)).c_str());
    std::printf("  \"instanceChallenge\": %s,\n", JsonString(ToHex(instanceChallenge)).c_str());
    std::printf("  \"buildAuthKey\": %s,\n", JsonString(ToHex(buildAuthKey)).c_str());
    std::printf("  \"continuedKey\": \"0x%016llX\",\n", (unsigned long long)continuedKey);
    std::printf("  \"sessionKey\": %s,\n", JsonString(ToHex(sessionKey.data(), sessionKey.size())).c_str());
    std::printf("  \"encryptKey\": %s,\n", JsonString(ToHex(encryptKey.data(), encryptKey.size())).c_str());
    std::printf("  \"authDigest\": %s,\n", JsonString(ToHex(authDigest.data(), authDigest.size())).c_str());
    std::printf("  \"continuedDigest\": %s,\n", JsonString(ToHex(continuedDigest.data(), continuedDigest.size())).c_str());
    std::printf("  \"enableEncryptionInput\": %s,\n", JsonString(ToHex(enableInput.data(), enableInput.size())).c_str());

    // ---- AES-GCM frames
    std::printf("  \"gcm\": [\n");
    Bytes payload = Sequence(0x10, 20);
    Bytes plaintext;
    plaintext.push_back(0x01); plaintext.push_back(0x00); plaintext.push_back(0x47); plaintext.push_back(0x00); // opcode 0x470001
    plaintext.insert(plaintext.end(), payload.begin(), payload.end());
    {
        WorldPacketCrypt crypt;
        WorldPacketCrypt::Key key = { };
        std::memcpy(key.data(), encryptKey.data(), key.size());
        crypt.Init(key);
        for (uint32 counter = 0; counter < 4; ++counter)
        {
            uint8 tag[12] = { };
            Bytes buffer = plaintext;
            if (!crypt.EncryptSend(buffer.data(), buffer.size(), tag))
            {
                std::fprintf(stderr, "EncryptSend failed\n");
                return 1;
            }
            // the server path is only a vector if the same key decrypts it again
            {
                AES decrypt(false, 256);
                decrypt.Init(std::span<uint8 const>(encryptKey));
                Bytes check = buffer;
                auto iv = MakeIV(counter, 0x52565253); // "SRVR"
                if (!decrypt.Process(iv, check.data(), check.size(), tag) || check != plaintext)
                {
                    std::fprintf(stderr, "server-direction GCM vector does not decrypt\n");
                    return 1;
                }
            }
            PrintGCM("server", counter, plaintext, buffer, tag);
        }
    }
    {
        WorldPacketCrypt verifier;
        WorldPacketCrypt::Key key = { };
        std::memcpy(key.data(), encryptKey.data(), key.size());
        verifier.Init(key);
        AES aes(true, 256);
        aes.Init(std::span<uint8 const>(encryptKey));
        for (uint32 counter = 0; counter < 4; ++counter)
        {
            uint8 tag[12] = { };
            Bytes buffer = plaintext;
            auto iv = MakeIV(counter, 0x544E4C43); // "CLNT"
            if (!aes.Process(iv, buffer.data(), buffer.size(), tag))
            {
                std::fprintf(stderr, "AES::Process failed\n");
                return 1;
            }
            {
                Bytes check = buffer;
                if (!verifier.DecryptRecv(check.data(), check.size(), tag) || check != plaintext)
                {
                    std::fprintf(stderr, "client-direction GCM vector is not accepted by WorldPacketCrypt::DecryptRecv\n");
                    return 1;
                }
            }
            PrintGCM("client", counter, plaintext, buffer, tag);
        }
    }
    std::printf("\n  ],\n");

    // ---- the first encrypted frame of each direction: two plain frames advance the same counter, so it is 2
    {
        WorldPacketCrypt::Key key = { };
        std::memcpy(key.data(), encryptKey.data(), key.size());
        uint8 tag[12] = { };

        // server -> client: AUTH_CHALLENGE and ENTER_ENCRYPTED_MODE went out before Init (WorldSocket.cpp:1156)
        WorldPacketCrypt server;
        Bytes scratch = plaintext;
        for (int i = 0; i < 2; ++i)
        {
            if (!server.EncryptSend(scratch.data(), scratch.size(), tag))
            {
                std::fprintf(stderr, "plain EncryptSend failed\n");
                return 1;
            }
        }
        server.Init(key);
        Bytes serverFrame = plaintext;
        uint8 serverTag[12] = { };
        if (!server.EncryptSend(serverFrame.data(), serverFrame.size(), serverTag))
        {
            std::fprintf(stderr, "first encrypted EncryptSend failed\n");
            return 1;
        }
        {
            AES decrypt(false, 256);
            decrypt.Init(std::span<uint8 const>(encryptKey));
            Bytes check = serverFrame;
            auto iv = MakeIV(2, 0x52565253);
            if (!decrypt.Process(iv, check.data(), check.size(), serverTag) || check != plaintext)
            {
                std::fprintf(stderr, "first server-direction frame does not decrypt at counter 2\n");
                return 1;
            }
        }

        // client -> server: AUTH_SESSION and the encrypted-mode ack went out before Init
        WorldPacketCrypt client;
        Bytes clientScratch = plaintext;
        for (int i = 0; i < 2; ++i)
        {
            if (!client.DecryptRecv(clientScratch.data(), clientScratch.size(), tag))
            {
                std::fprintf(stderr, "plain DecryptRecv failed\n");
                return 1;
            }
        }
        client.Init(key);
        AES aes(true, 256);
        aes.Init(std::span<uint8 const>(encryptKey));
        Bytes clientFrame = plaintext;
        uint8 clientTag[12] = { };
        if (!aes.Process(MakeIV(2, 0x544E4C43), clientFrame.data(), clientFrame.size(), clientTag))
        {
            std::fprintf(stderr, "client frame encryption at counter 2 failed\n");
            return 1;
        }
        {
            Bytes check = clientFrame;
            if (!client.DecryptRecv(check.data(), check.size(), clientTag) || check != plaintext)
            {
                std::fprintf(stderr, "the server's DecryptRecv does not accept the client frame at counter 2\n");
                return 1;
            }
        }

        std::printf("  \"firstEncryptedFrames\": {\n");
        std::printf("    \"plainFramesBefore\": 2,\n");
        std::printf("    \"counter\": 2,\n");
        std::printf("    \"plaintext\": %s,\n", JsonString(ToHex(plaintext)).c_str());
        std::printf("    \"server\": { \"tag\": %s, \"ciphertext\": %s },\n",
            JsonString(ToHex(serverTag, 12)).c_str(), JsonString(ToHex(serverFrame)).c_str());
        std::printf("    \"client\": { \"tag\": %s, \"ciphertext\": %s }\n",
            JsonString(ToHex(clientTag, 12)).c_str(), JsonString(ToHex(clientFrame)).c_str());
        std::printf("  },\n");
    }

    // ---- compression: two packets in one stream, the way WorldSocket::CompressPacket writes them
    std::printf("  \"compression\": [\n");
    {
        z_stream stream = { };
        if (deflateInit2(&stream, 1, Z_DEFLATED, -15, 8, Z_DEFAULT_STRATEGY) != Z_OK)
        {
            std::fprintf(stderr, "deflateInit2 failed\n");
            return 1;
        }
        struct Packet
        {
            uint32 Opcode;
            Bytes Payload;
        };
        std::vector<Packet> packets = {
            { 0x4E000E, Bytes(1200, 'A') },
            { 0x47001F, Bytes(600, 'A') },
        };
        for (size_t i = 0; i < packets.size(); ++i)
        {
            uint32 opcode = packets[i].Opcode;
            uint32 uncompressedSize = uint32(packets[i].Payload.size()) + 4;
            uint32 uncompressedAdler = adler32(adler32(CompressionAdlerSeed, (Bytef*)&opcode, 4), packets[i].Payload.data(), uint32(packets[i].Payload.size()));
            uint32 bufferSize = deflateBound(&stream, uncompressedSize);
            Bytes out(bufferSize);
            stream.next_out = out.data();
            stream.avail_out = bufferSize;
            stream.next_in = (Bytef*)&opcode;
            stream.avail_in = 4;
            if (deflate(&stream, Z_NO_FLUSH) != Z_OK)
            {
                std::fprintf(stderr, "deflate opcode failed\n");
                return 1;
            }
            stream.next_in = packets[i].Payload.data();
            stream.avail_in = uint32(packets[i].Payload.size());
            if (deflate(&stream, Z_SYNC_FLUSH) != Z_OK)
            {
                std::fprintf(stderr, "deflate payload failed\n");
                return 1;
            }
            out.resize(bufferSize - stream.avail_out);
            uint32 compressedAdler = adler32(CompressionAdlerSeed, out.data(), uint32(out.size()));
            std::printf("%s    { \"uncompressedSize\": %u, \"uncompressedAdler\": \"0x%08X\", \"compressedAdler\": \"0x%08X\", \"innerOpcode\": \"0x%08X\", \"plaintext\": %s, \"compressed\": %s }\n",
                i ? ",\n" : "", uncompressedSize, uncompressedAdler, compressedAdler, opcode, JsonString(ToHex(packets[i].Payload)).c_str(),
                JsonString(ToHex(out)).c_str());
        }
        deflateEnd(&stream);
    }
    std::printf("\n  ]\n");
    std::printf("}\n");
    return 0;
}
