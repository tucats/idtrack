#!/usr/bin/env swift
//
// generate_icon.swift
// Renders the IDTrack app icon as a 1024x1024 PNG.
// Run: swift generate_icon.swift <output.png>
//
// The icon is a blue background with a white brain outline and the letters "ID"
// in the center. Designed to meet Apple's macOS/iOS 26 app icon guidance:
// solid, opaque shapes on a full-bleed background; the system applies Liquid
// Glass masking, specular highlights, and rounded-rect masking automatically
// when the image is placed in an AppIcon asset catalog.
//

import AppKit
import CoreGraphics
import CoreText
import ImageIO
import UniformTypeIdentifiers

guard CommandLine.arguments.count > 1 else {
    FileHandle.standardError.write(Data("usage: generate_icon.swift <output.png>\n".utf8))
    exit(2)
}
let outputPath = CommandLine.arguments[1]

let size: CGFloat = 1024
let cx: CGFloat = size / 2
let cy: CGFloat = size / 2

let colorSpace = CGColorSpaceCreateDeviceRGB()
guard let ctx = CGContext(
    data: nil,
    width: Int(size),
    height: Int(size),
    bitsPerComponent: 8,
    bytesPerRow: 0,
    space: colorSpace,
    bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue
) else {
    fatalError("CGContext create failed")
}

// ---------------------------------------------------------------
// 1. Blue background — top-lit gradient so the icon reads with a
//    subtle sense of depth before any system effects apply.
// ---------------------------------------------------------------
let bgColors = [
    CGColor(srgbRed: 0.30, green: 0.60, blue: 1.00, alpha: 1.0), // top
    CGColor(srgbRed: 0.04, green: 0.20, blue: 0.66, alpha: 1.0), // bottom
] as CFArray
let bgGradient = CGGradient(colorsSpace: colorSpace, colors: bgColors, locations: [0.0, 1.0])!
ctx.drawLinearGradient(
    bgGradient,
    start: CGPoint(x: 0, y: size),
    end: CGPoint(x: 0, y: 0),
    options: []
)

// Soft radial highlight near the top — adds dimensionality.
let hlColors = [
    CGColor(srgbRed: 1, green: 1, blue: 1, alpha: 0.18),
    CGColor(srgbRed: 1, green: 1, blue: 1, alpha: 0.0),
] as CFArray
let hlGradient = CGGradient(colorsSpace: colorSpace, colors: hlColors, locations: [0.0, 1.0])!
ctx.drawRadialGradient(
    hlGradient,
    startCenter: CGPoint(x: size * 0.5, y: size * 0.88),
    startRadius: 0,
    endCenter: CGPoint(x: size * 0.5, y: size * 0.88),
    endRadius: size * 0.7,
    options: []
)

// ---------------------------------------------------------------
// 2. Brain outline. Use the SF Symbol "brain" so the artwork is a
//    well-designed vector representation of a human brain.
// ---------------------------------------------------------------
guard let brain = NSImage(systemSymbolName: "brain", accessibilityDescription: nil) else {
    fatalError("SF Symbol 'brain' is unavailable on this system")
}

var symbolConfig = NSImage.SymbolConfiguration(pointSize: 760, weight: .regular)
symbolConfig = symbolConfig.applying(NSImage.SymbolConfiguration(paletteColors: [.white]))
guard let tintedBrain = brain.withSymbolConfiguration(symbolConfig) else {
    fatalError("Symbol configuration failed")
}

let bSize = tintedBrain.size
let brainRect = CGRect(
    x: (size - bSize.width) / 2,
    y: (size - bSize.height) / 2,
    width: bSize.width,
    height: bSize.height
)

guard let brainCG = tintedBrain.cgImage(forProposedRect: nil, context: nil, hints: nil) else {
    fatalError("brain.cgImage failed")
}

// Drop a soft shadow under the brain so it sits cleanly on the
// blue and gives the system something to refract.
ctx.saveGState()
ctx.setShadow(
    offset: CGSize(width: 0, height: -8),
    blur: 24,
    color: CGColor(srgbRed: 0, green: 0, blue: 0, alpha: 0.25)
)
ctx.draw(brainCG, in: brainRect)
ctx.restoreGState()

// ---------------------------------------------------------------
// 3. "ID" letters in the center of the brain. Heavy weight, white,
//    with a tight kern so the two glyphs read as a single mark.
// ---------------------------------------------------------------
let font = NSFont.systemFont(ofSize: 320, weight: .heavy)
let textAttrs: [NSAttributedString.Key: Any] = [
    .font: font,
    .foregroundColor: NSColor.white,
    .kern: -8,
]
let attrText = NSAttributedString(string: "ID", attributes: textAttrs)
let line = CTLineCreateWithAttributedString(attrText)
let bounds = CTLineGetBoundsWithOptions(line, .useGlyphPathBounds)

// Draw a subtle dark wash behind the letters so they read clearly
// over the brain outline.
let washRect = CGRect(
    x: cx - bounds.width / 2 - 60,
    y: cy - bounds.height / 2 - 30,
    width: bounds.width + 120,
    height: bounds.height + 60
)
ctx.saveGState()
ctx.setFillColor(CGColor(srgbRed: 0.04, green: 0.16, blue: 0.50, alpha: 0.55))
let washPath = CGPath(
    roundedRect: washRect,
    cornerWidth: washRect.height / 2,
    cornerHeight: washRect.height / 2,
    transform: nil
)
ctx.addPath(washPath)
ctx.fillPath()
ctx.restoreGState()

// Letters
ctx.saveGState()
ctx.setShadow(
    offset: CGSize(width: 0, height: -6),
    blur: 14,
    color: CGColor(srgbRed: 0, green: 0, blue: 0, alpha: 0.35)
)
ctx.textPosition = CGPoint(
    x: cx - bounds.midX,
    y: cy - bounds.midY
)
CTLineDraw(line, ctx)
ctx.restoreGState()

// ---------------------------------------------------------------
// 4. Write PNG.
// ---------------------------------------------------------------
guard let img = ctx.makeImage() else { fatalError("makeImage failed") }
let outURL = URL(fileURLWithPath: outputPath)
guard let dest = CGImageDestinationCreateWithURL(
    outURL as CFURL,
    UTType.png.identifier as CFString,
    1,
    nil
) else { fatalError("destination create failed") }
CGImageDestinationAddImage(dest, img, nil)
guard CGImageDestinationFinalize(dest) else { fatalError("destination finalize failed") }

print("wrote \(outURL.path) (\(Int(size))x\(Int(size)))")
