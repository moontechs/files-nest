#if canImport(CryptoKit)
import CryptoKit
#else
import Crypto
#endif
import Foundation

/// Returns the stable, URL-safe identifier used as the organized filename suffix.
///
/// The input is hashed as its raw UTF-8 representation, without Unicode
/// normalization, to match the server's SHA-256 implementation byte-for-byte.
func safeID(_ input: String) -> String {
    let digest = SHA256.hash(data: Data(input.utf8))
    return Data(digest)
        .base64EncodedString()
        .replacingOccurrences(of: "+", with: "-")
        .replacingOccurrences(of: "/", with: "_")
        .replacingOccurrences(of: "=", with: "")
}
