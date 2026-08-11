// swift-tools-version: 6.0
import PackageDescription

let package = Package(
    name: "FilesNestCore",
    platforms: [.macOS(.v13), .iOS(.v17)],
    products: [
        .library(name: "FilesNestCore", targets: ["FilesNestCore"]),
    ],
    targets: [
        .target(name: "FilesNestCore"),
        .testTarget(name: "FilesNestCoreTests", dependencies: ["FilesNestCore"]),
    ]
)
