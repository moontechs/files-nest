// swift-tools-version: 6.0
import PackageDescription

let package = Package(
    name: "FilesNestCore",
    platforms: [.macOS(.v13), .iOS(.v17)],
    products: [
        .library(name: "FilesNestCore", targets: ["FilesNestCore"]),
    ],
    dependencies: [
        // CryptoKit stand-in on Linux (no such framework there); SafeID.swift
        // prefers CryptoKit on Apple platforms and falls back to this package's
        // source-compatible `Crypto` module elsewhere.
        .package(url: "https://github.com/apple/swift-crypto.git", from: "3.0.0"),
    ],
    targets: [
        .target(
            name: "FilesNestCore",
            dependencies: [
                .product(name: "Crypto", package: "swift-crypto", condition: .when(platforms: [.linux])),
            ]
        ),
        .testTarget(name: "FilesNestCoreTests", dependencies: ["FilesNestCore"]),
    ]
)
