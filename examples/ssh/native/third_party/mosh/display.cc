// Terminal::Display's constructor without terminfo, in place of
// terminaldisplayinit.cc (curses): the client uses a Display only inside
// Terminal::Complete.
#include "terminaldisplay.h"

Terminal::Display::Display(bool)
    : has_ech(true), has_bce(true), has_title(true), smcup(nullptr), rmcup(nullptr) {}
