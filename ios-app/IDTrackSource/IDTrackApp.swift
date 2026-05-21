import SwiftUI

// MARK: - App entry point
//
// `@main` is the Swift attribute that designates this type as the application's
// entry point. The compiler generates the `main()` function that iOS calls at
// launch. Only one type in the whole project may carry @main.
//
// The `App` protocol requires one property: `body: some Scene`. A Scene
// defines a top-level container for the UI — on iPhone/iPad that is typically
// a WindowGroup (one window), but macOS also supports multiple windows.

@main
struct IDTrackApp: App {

    // `@StateObject` creates an ObservableObject instance and ties its lifetime
    // to this view. Because IDTrackApp is the root of the hierarchy, AppState
    // lives for the entire run of the app.
    //
    // Contrast with `@ObservedObject`: that annotation observes an object
    // created elsewhere and does NOT own its lifetime — the object could be
    // deallocated while the view still holds a reference. At the root level,
    // always use @StateObject.
    @StateObject private var appState = AppState()

    var body: some Scene {
        WindowGroup {
            ContentView()
                // `.environmentObject` injects appState into the entire view
                // hierarchy. Any descendant view can access it with:
                //   @EnvironmentObject var appState: AppState
                // without needing to pass it down through every intermediate view.
                .environmentObject(appState)
                // Apply the user's dark/light mode preference to the whole app.
                // SwiftUI re-evaluates this when appState.darkMode changes.
                .preferredColorScheme(appState.darkMode ? .dark : .light)
        }
    }
}
