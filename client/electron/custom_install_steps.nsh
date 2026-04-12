; Copyright 2018 The Outline Authors
;
; Licensed under the Apache License, Version 2.0 (the "License");
; you may not use this file except in compliance with the License.
; You may obtain a copy of the License at
;
;      http://www.apache.org/licenses/LICENSE-2.0
;
; Unless required by applicable law or agreed to in writing, software
; distributed under the License is distributed on an "AS IS" BASIS,
; WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
; See the License for the specific language governing permissions and
; limitations under the License.

!include StrFunc.nsh
!include WinVer.nsh
!include x64.nsh

!include env.nsh

; StrFunc weirdness; this fix suggested here:
; https://github.com/electron-userland/electron-builder/issues/888
!ifndef BUILD_UNINSTALLER
${StrLoc}
${StrNSISToIO}
${StrRep}
!endif

!macro customInstall
  ; SOCKS5 mode does not require administrator permissions or special drivers.
!macroend

!macro customUnInstall
  ; Nothing to clean up for SOCKS5 mode.
!macroend
