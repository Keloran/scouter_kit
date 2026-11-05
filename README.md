<img src="examples/ssh/icon.png" style="float:right; max-width: 50%; margin: 0 0 .1em .5em">

# ScouterKit

ScouterKit is a response to a widespread pattern where smart glasses are "locked in" to some pseudo-OS - and can only access "apps" produced for that "OS". I think that this model only really serves the OS authors - which can then control their own mini ecosystem and extort its users. Pretty much everybode else would be better off if we had a bunch of standardized, cross-compatible tools that can work with any glasses and are not locked to any platform.

ScouterKit is exactly this - a set of tools for taking control of your smart glasses.

<table>
  <tr>
    <td><video src="VID_20261105_BAD_APPLE.mp4" controls muted width="100%"></video></td>
    <td><video src="VID_20261105_SSH.mp4" controls muted width="100%"></video></td>
  </tr>
  <tr>
    <td align="center">Real-time video playback</td>
    <td align="center">SSH terminal in the glasses</td>
  </tr>
  <tr>
    <td align="center"><code>go/examples/videoplay</code></td>
    <td align="center"><code>examples/ssh/</code></td>
  </tr>
</table>

**Disclaimer:** This is personal project and it only supports one model of smart glasses (Even Realities G1). Nonetheless it's designed with extensibility in mind & shared in the open with the hopes that other tinkerers will find it useful.

ScouterKit provides three things:

1. Alternative firmware for the Even Realities G1 smart glasses that offers direct access to all of their features (no need for a companion app)
2. Standardized protocol for interacting with the glasses - a VM that runs on the glasses & a set of instructions for controlling & reprogramming it over-the-air
3. A collection of flexible libraries (C/C++, Go, Python, Android), demos & utilities that can talk to the glasses and make app development easier

The idea is that once you have a simple VM on the glasses then various utilities (executed on the PC, phone or even other IoT devices) can then install mini-programs that would then run on autonomously the glasses. These mini-programs can still talk with other devices (even the two sides of the glasses can talk to each other!) and can control smart glasses in however they want.

## ScouterKit Firmware for Even Realities G1

Features:

* does not record you 24/7 (which the stock firmware "accidentally" does)
* exposes the full 640x480 px display surface (~4x more pixels than 576x136px offered by the stock firmware)
* fast bitmap transfers (allows 30fps video playback vs 6 seconds to transfer a single bloody bitmap on the stock - and that doesn't even account for the 4x higher resolution)
* horizontal viewport extension - extends the effective screen area by drawing wider image (left eye sees more on the left, right - on the right)
* vertical viewport alignment - helps with left/right image fusion (compensates the misalignment in how the glasses sit on your nose)
* real-time access to IMU - allowing stereoscopic 3D rendering on the glasses (see `go/examples/cube`)

## ScouterKit Protocol

In ScouterKit every eye implements as a tiny programmable VM. VM Commands may be sent over Bluetooth - so real-time control from another device is possible. But the same commands may also be uploaded and stored in the on-board memory for autonomous operation.

Features:

* common drawing commands
* standard glasses control (turning panels on/off, setting brightness, collecting stats)
* uploading bitmaps to glasses as reusable textures
* uploading & drawing text with custom fonts
* left-right eye display synchronization

## ScouterKit Libraries

Libraries are designed to be adaptable - they come with the firmware & an emulator - which allows new VM operations to be safely added/removed.

The libraries are provided for the following languages:

* C/C++ core (provided for Linux + BlueZ)
* Go (wrapper around C/C++)
* Python (wrapper around C/C++)
* Java (only as part of the SSH example)

# ScouterKit + Keyer

<video src="VID_20261105_KEYER.mp4" controls muted width="40%" style="float:right"></video>

ScouterKit works well with a one-handed keyboard called *Keyer*. This is the basis for my mobile SSH terminal.

I'd like to show it as an example of the stuff that you can build with ScouterKit.

You can find its sources at `examples/ssh` - but it really needs the custom hardware (which you can make yourself - check [this link](https://github.com/mafik/keyer)!) to unlock its true potential!

# Project structure

* `font_converter/` - python script that converts `.ttf` files into ScouterKit-compatible packed font files (glasses have very little memory so something like this is necessary)
* `g1_firmware/` - firmware for the Even Realities G1
* `src/` - C/C++ sources - the core library that talks with the glasses
* `go/` - Golang wrappers
* `tests/`, `third_party/` - extra code for the C/C++ library
* `tools/` - tools for testing the glasses

# Editing

Most of the sources in this directory have been auto-generated using LLMs. LLMs tend to generate verbose & poorly engineered solutions. While I've tried to compensate for the latter with some common sense design direction, you should consider your own mental wellbeing and instead of trying to read/modify these sources directly - you should use another LLM to do that instead.

Pull requests are welcome.

Please keep LLMs out of these files:
* `README.md`
* `DESIGN.md`
