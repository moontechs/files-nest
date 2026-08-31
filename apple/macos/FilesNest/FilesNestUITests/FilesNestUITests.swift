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
        app.launchArguments = ["-uiTesting"]
        app.launch()
    }

    @MainActor
    func testFirstRunGuidesTheUserThroughDestinationSetup() throws {
        let setup = app.buttons["panel.setup"]
        XCTAssertTrue(setup.waitForExistence(timeout: 5))
        setup.click()

        let serverURL = app.textFields["settings.serverURL"]
        XCTAssertTrue(serverURL.waitForExistence(timeout: 5))
        XCTAssertEqual(serverURL.value as? String, "")
        XCTAssertEqual(app.textFields["settings.username"].value as? String, "")
        XCTAssertEqual(app.secureTextFields["settings.password"].value as? String, "")

        let destinations = app.segmentedControls["settings.destination"]
        destinations.buttons["Local Folder"].click()

        XCTAssertTrue(app.staticTexts["No folder selected"].waitForExistence(timeout: 2))
        XCTAssertTrue(app.buttons["settings.chooseFolder"].exists)
    }

    @MainActor
    func testInvalidServerURLShowsValidationError() throws {
        let setup = app.buttons["panel.setup"]
        XCTAssertTrue(setup.waitForExistence(timeout: 5))
        setup.click()

        let serverURL = app.textFields["settings.serverURL"]
        XCTAssertTrue(serverURL.waitForExistence(timeout: 5))
        serverURL.click()
        serverURL.typeText("not a URL")
        app.textFields["settings.username"].click()
        app.textFields["settings.username"].typeText("michael")
        app.secureTextFields["settings.password"].click()
        app.secureTextFields["settings.password"].typeText("secret")

        app.buttons["settings.connect"].click()

        let error = app.staticTexts["settings.error"]
        XCTAssertTrue(error.waitForExistence(timeout: 2))
        XCTAssertEqual(error.label, "Enter a valid server URL.")
    }
}
