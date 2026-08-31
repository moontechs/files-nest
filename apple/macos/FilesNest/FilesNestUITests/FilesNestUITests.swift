//
//  FilesNestUITests.swift
//  FilesNestUITests
//
//  Created by Paulo Garcia on 23.07.26.
//

import XCTest

final class FilesNestUITests: XCTestCase {
    private var app: XCUIApplication!

    override func setUpWithError() throws {
        continueAfterFailure = false
        app = XCUIApplication()
    }

    @MainActor
    func testUnauthorizedServerShowsCredentialGuidance() throws {
        launch()

        openSettings()
        enterServerCredentials(serverURL: "https://unauthorized.filesnest.test")
        app.buttons["settings.connect"].click()

        let result = app.staticTexts["settings.connectionResult"]
        XCTAssertTrue(result.waitForExistence(timeout: 2))
        XCTAssertEqual(result.value as? String, "The server rejected these credentials.")
    }

    @MainActor
    func testUnreachableServerShowsNetworkGuidance() throws {
        launch()

        openSettings()
        enterServerCredentials(serverURL: "https://unreachable.filesnest.test")
        app.buttons["settings.connect"].click()

        let result = app.staticTexts["settings.connectionResult"]
        XCTAssertTrue(result.waitForExistence(timeout: 2))
        XCTAssertEqual(result.value as? String, "Couldn’t reach the server.")
    }

    @MainActor
    func testChoosingAScriptedLocalFolderDisplaysItsPath() throws {
        launch()

        openSettings()
        app.radioGroups["settings.destination"].radioButtons["Local Folder"].click()
        app.buttons["settings.chooseFolder"].click()

        let selectedFolder = app.staticTexts["settings.selectedFolder"]
        XCTAssertTrue(selectedFolder.waitForExistence(timeout: 2))
        let selectedPath = selectedFolder.value as? String ?? ""
        XCTAssertTrue((selectedPath as NSString).hasSuffix("FilesNestUITestsLocalFolder"),
                      "Selected folder path: \(selectedPath)")
    }

    @MainActor
    func testSuccessfulConnectionEnablesTheOperationalPanel() throws {
        launch()
        openSettings()
        enterServerCredentials()
        app.buttons["settings.connect"].click()

        let result = app.staticTexts["settings.connectionResult"]
        XCTAssertTrue(result.waitForExistence(timeout: 2))
        XCTAssertEqual(result.value as? String, "Connected")

        app.typeKey("w", modifierFlags: .command)
        openPanel()
        XCTAssertTrue(app.buttons["panel.syncNow"].waitForExistence(timeout: 3))
        XCTAssertTrue(app.buttons["panel.pauseResume"].exists)
    }

    @MainActor
    func testSyncingPanelCanPauseAndResume() throws {
        launch(fixture: "syncing")
        openPanel()

        XCTAssertEqual(app.staticTexts["panel.title"].value as? String, "Syncing…")
        XCTAssertEqual(app.staticTexts["panel.status"].value as? String, "Backing up")
        app.buttons["panel.pauseResume"].click()
        XCTAssertTrue(app.buttons["panel.pauseResume"].waitForExistence(timeout: 2))
        XCTAssertEqual(app.buttons["panel.pauseResume"].label, "Resume")
        XCTAssertEqual(app.staticTexts["panel.title"].value as? String, "Paused")

        app.buttons["panel.pauseResume"].click()
        XCTAssertEqual(app.staticTexts["panel.title"].value as? String, "Syncing…")
    }

    @MainActor
    func testErrorPanelRetryStartsSyncing() throws {
        launch(fixture: "error")
        openPanel()

        XCTAssertEqual(app.staticTexts["panel.status"].value as? String, "Attention needed")
        app.buttons["panel.retry"].click()
        XCTAssertEqual(app.staticTexts["panel.title"].value as? String, "Syncing…")
    }

    @MainActor
    func testFailedItemsCanBeInspectedAndRetried() throws {
        launch(fixture: "failed")
        openPanel()

        let failedItems = app.buttons["panel.failedItems"]
        XCTAssertTrue(failedItems.waitForExistence(timeout: 3))
        failedItems.click()
        let filename = app.staticTexts["IMG_2045.HEIC"]
        XCTAssertTrue(filename.waitForExistence(timeout: 2))
        XCTAssertTrue(app.staticTexts["The server is unavailable."].exists)

        app.buttons["failed.retry"].click()
        XCTAssertEqual(app.staticTexts["panel.title"].value as? String, "Syncing…")
    }

    private func launch(fixture: String? = nil) {
        app = XCUIApplication()
        app.launchArguments = ["-uiTesting", "YES"]
        if let fixture { app.launchArguments += ["-uiFixture", fixture] }
        app.launch()
    }

    private func openPanel() {
        let statusItem = app.statusItems.firstMatch
        XCTAssertTrue(statusItem.waitForExistence(timeout: 5))
        statusItem.click()
    }

    private func openSettings() {
        openPanel()

        let setup = app.buttons["panel.setup"]
        if !setup.waitForExistence(timeout: 2) {
            openPanel()
        }
        XCTAssertTrue(setup.waitForExistence(timeout: 5))
        setup.click()
        XCTAssertTrue(app.textFields["settings.serverURL"].waitForExistence(timeout: 5))
    }

    private func enterServerCredentials(serverURL: String = "https://filesnest.test") {
        let serverURLField = app.textFields["settings.serverURL"]
        serverURLField.click()
        serverURLField.typeText(serverURL)
        app.textFields["settings.username"].click()
        app.textFields["settings.username"].typeText("michael")
        app.secureTextFields["settings.password"].click()
        app.secureTextFields["settings.password"].typeText("secret")
    }

    @MainActor
    func testFirstRunGuidesTheUserThroughDestinationSetup() throws {
        launch()
        openSettings()

        let serverURL = app.textFields["settings.serverURL"]
        XCTAssertEqual(serverURL.value as? String, "")
        XCTAssertEqual(app.textFields["settings.username"].value as? String, "")
        XCTAssertEqual(app.secureTextFields["settings.password"].value as? String, "")

        app.radioGroups["settings.destination"].radioButtons["Local Folder"].click()

        XCTAssertTrue(app.staticTexts["No folder selected"].waitForExistence(timeout: 2))
        XCTAssertTrue(app.buttons["settings.chooseFolder"].exists)
    }

    @MainActor
    func testInvalidServerURLShowsValidationError() throws {
        launch()
        openSettings()

        let serverURL = app.textFields["settings.serverURL"]
        serverURL.click()
        serverURL.typeText("not a URL")
        app.textFields["settings.username"].click()
        app.textFields["settings.username"].typeText("michael")
        app.secureTextFields["settings.password"].click()
        app.secureTextFields["settings.password"].typeText("secret")

        app.buttons["settings.connect"].click()

        let error = app.staticTexts["settings.error"]
        XCTAssertTrue(error.waitForExistence(timeout: 2))
        XCTAssertEqual(error.value as? String, "Enter a valid server URL.")
    }
}
