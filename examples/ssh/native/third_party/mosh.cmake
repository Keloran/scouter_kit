# mosh's client parts from the pinned source (build.sh): network, crypto,
# statesync, terminal and the prediction overlay, with its protocol buffers.
# An object library, so terminalfunctions.cc's static registrations link.
# Needs MOSH_SRC, PROTOC and the targets protobuf-lite and crypto.
set(mosh_gen ${CMAKE_CURRENT_BINARY_DIR}/mosh_protobufs)
file(MAKE_DIRECTORY ${mosh_gen})
foreach(p hostinput transportinstruction userinput)
  add_custom_command(OUTPUT ${mosh_gen}/${p}.pb.cc ${mosh_gen}/${p}.pb.h
    COMMAND ${PROTOC} --cpp_out=${mosh_gen} -I${MOSH_SRC}/src/protobufs ${MOSH_SRC}/src/protobufs/${p}.proto
    DEPENDS ${MOSH_SRC}/src/protobufs/${p}.proto)
  list(APPEND mosh_sources ${mosh_gen}/${p}.pb.cc)
endforeach()
foreach(f crypto/base64 crypto/crypto crypto/ocb_openssl
    network/compressor network/network network/transportfragment
    statesync/completeterminal statesync/user
    terminal/parser terminal/parseraction terminal/parserstate terminal/terminal
    terminal/terminaldispatcher terminal/terminaldisplay terminal/terminalframebuffer
    terminal/terminalfunctions terminal/terminaluserinput
    frontend/terminaloverlay
    util/locale_utils util/timestamp)
  list(APPEND mosh_sources ${MOSH_SRC}/src/${f}.cc)
endforeach()
add_library(mosh OBJECT ${mosh_sources} ${CMAKE_CURRENT_LIST_DIR}/mosh/display.cc)
set_target_properties(mosh PROPERTIES CXX_STANDARD 17 POSITION_INDEPENDENT_CODE ON)
target_include_directories(mosh PUBLIC ${CMAKE_CURRENT_LIST_DIR}/mosh ${mosh_gen}
  ${MOSH_SRC}/src/crypto ${MOSH_SRC}/src/network ${MOSH_SRC}/src/statesync ${MOSH_SRC}/src/terminal
  ${MOSH_SRC}/src/frontend ${MOSH_SRC}/src/util)
target_compile_options(mosh PRIVATE -Wno-unused-parameter -Wno-deprecated-declarations)
target_link_libraries(mosh PUBLIC protobuf-lite crypto z)
