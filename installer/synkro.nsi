; Installateur de Synkro (NSIS 3, Modern UI 2)
; makensis -DVERSION=0.1.0 -DEXE=chemin\Synkro.exe synkro.nsi
;
; L'installateur s'ouvre sans droits d'administrateur. Quand le dossier choisi
; en demande (C:\Program Files…), il se relance lui-même en administrateur,
; sans fenêtre (/S /SUB), juste pour copier les fichiers et inscrire Synkro dans
; Paramètres › Applications. Les raccourcis et le lancement final restent faits
; par l'utilisateur, sans droits. La désinstallation fonctionne de la même façon.

Unicode true
!include "MUI2.nsh"
!include "nsDialogs.nsh"
!include "LogicLib.nsh"
!include "FileFunc.nsh"
!include "x64.nsh"

!ifndef VERSION
  !define VERSION "0.1.0"
!endif
!ifndef EXE
  !define EXE "Synkro.exe"
!endif
!define APP "Synkro"
!define UNINST_KEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\Synkro"
!define UNINST_EXE "Désinstaller Synkro.exe"

Name "${APP} ${VERSION}"
OutFile "SynkroSetup-${VERSION}.exe"
RequestExecutionLevel user
InstallDir "$PROGRAMFILES64\Synkro"
SetCompressor /SOLID lzma
BrandingText "${APP} ${VERSION}"
VIProductVersion "${VERSION}.0"
VIAddVersionKey /LANG=1036 "ProductName" "${APP}"
VIAddVersionKey /LANG=1036 "FileDescription" "Installation de ${APP}"
VIAddVersionKey /LANG=1036 "FileVersion" "${VERSION}"
VIAddVersionKey /LANG=1036 "ProductVersion" "${VERSION}"
VIAddVersionKey /LANG=1036 "LegalCopyright" "${APP}"

!define MUI_ICON "synkro.ico"
!define MUI_UNICON "synkro.ico"
!define MUI_ABORTWARNING

; ---------- variables ----------
Var Portable      ; 1 = mode portable
Var WantSM        ; 1 = raccourci dans le menu Démarrer
Var WantDesk      ; 1 = raccourci sur le bureau
Var LastMode      ; mode pour lequel $INSTDIR a été proposé
Var DefInstalled  ; dossier proposé en mode installé
Var NeedAdmin     ; 1 = le dossier choisi demande les droits d'administrateur
Var Sub           ; 1 = instance relancée en administrateur (sans fenêtre)
Var Update        ; 1 = Synkro déjà installé : mise à jour
Var hInst
Var hPort
Var hSM
Var hDesk
Var hNote

; ---------- pages ----------
!define MUI_WELCOMEPAGE_TITLE "Bienvenue dans l'installation de ${APP} ${VERSION}"
!define MUI_WELCOMEPAGE_TEXT "${APP} garde deux dossiers à l'identique, ou en sauvegarde l'un vers l'autre : dossiers de ce PC, clé USB ou disque externe, serveur distant en SFTP.$\r$\n$\r$\n\
• Analyse d'abord : la liste de ce qui serait copié, supprimé ou en conflit, avant de toucher à quoi que ce soit.$\r$\n\
• Miroir, sauvegarde ou synchronisation dans les deux sens.$\r$\n\
• Corbeille : les fichiers remplacés ou supprimés restent récupérables.$\r$\n\
• Planification, groupes de tâches, fichiers verrouillés (Outlook…), pause et vitesse réglable pendant les transferts.$\r$\n$\r$\n\
Cliquez sur Suivant pour continuer."
!insertmacro MUI_PAGE_WELCOME
Page custom ModePage ModeLeave
!define MUI_PAGE_CUSTOMFUNCTION_PRE DirPre
!define MUI_PAGE_CUSTOMFUNCTION_SHOW DirShow
!define MUI_PAGE_CUSTOMFUNCTION_LEAVE DirLeave
!define MUI_DIRECTORYPAGE_TEXT_TOP "Dossier où installer ${APP}. Cliquez sur Parcourir pour en choisir un autre."
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!define MUI_FINISHPAGE_TITLE "Installation de ${APP} terminée"
!define MUI_FINISHPAGE_TEXT "${APP} est installé dans :$\r$\n$INSTDIR"
!define MUI_FINISHPAGE_RUN "$INSTDIR\Synkro.exe"
!define MUI_FINISHPAGE_RUN_TEXT "Lancer ${APP} maintenant"
!define MUI_FINISHPAGE_SHOWREADME "$INSTDIR\LISEZMOI.txt"
!define MUI_FINISHPAGE_SHOWREADME_TEXT "Afficher le fichier LISEZMOI"
!define MUI_FINISHPAGE_SHOWREADME_NOTCHECKED
!insertmacro MUI_PAGE_FINISH

!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES

!insertmacro MUI_LANGUAGE "French"

; ---------- outils ----------

; Synkro est-il lancé depuis ce dossier ? (son processus, pas un autre Synkro)
; résultat dans $R9 : 1 = oui
!macro IsRunning UN
Function ${UN}IsRunning
  StrCpy $R9 0
  ${If} ${FileExists} "$INSTDIR\Synkro.exe"
    ; code de sortie = 10 + nombre de Synkro lancés depuis ce dossier
    ; (le texte affiché par PowerShell n'est pas fiable)
    nsExec::Exec `powershell -NoProfile -NonInteractive -ExecutionPolicy Bypass -Command "exit (10 + @(Get-Process -Name Synkro -ErrorAction SilentlyContinue | Where-Object { $$_.Path -ieq '$INSTDIR\Synkro.exe' }).Count)"`
    Pop $R8
    ${If} $R8 == 10
      StrCpy $R9 0
    ${ElseIf} $R8 > 10
    ${AndIf} $R8 < 100
      StrCpy $R9 1
    ${Else}
      ; PowerShell indisponible : on tente d'ouvrir l'exécutable en écriture,
      ; seulement si le dossier est inscriptible (sinon on ne peut pas conclure)
      ClearErrors
      FileOpen $R6 "$INSTDIR\.synkro-test" w
      ${IfNot} ${Errors}
        FileClose $R6
        Delete "$INSTDIR\.synkro-test"
        ClearErrors
        FileOpen $R6 "$INSTDIR\Synkro.exe" a
        ${If} ${Errors}
          StrCpy $R9 1
        ${Else}
          FileClose $R6
        ${EndIf}
      ${EndIf}
    ${EndIf}
  ${EndIf}
FunctionEnd
!macroend
!insertmacro IsRunning ""
!insertmacro IsRunning "un."

; Peut-on écrire dans $INSTDIR sans droits d'administrateur ? $R9 : 1 = oui
!macro Writable UN
Function ${UN}Writable
  StrCpy $R9 0
  ClearErrors
  CreateDirectory "$INSTDIR"
  FileOpen $R6 "$INSTDIR\.synkro-test" w
  ${IfNot} ${Errors}
    FileClose $R6
    Delete "$INSTDIR\.synkro-test"
    StrCpy $R9 1
  ${EndIf}
FunctionEnd
!macroend
!insertmacro Writable ""
!insertmacro Writable "un."

; ---------- démarrage ----------
Function .onInit
  ${If} ${RunningX64}
    SetRegView 64
  ${EndIf}
  StrCpy $Portable 0
  StrCpy $WantSM 1
  StrCpy $WantDesk 1
  StrCpy $LastMode ""
  StrCpy $NeedAdmin 0
  StrCpy $Update 0
  StrCpy $Sub 0
  ${GetParameters} $R0
  ClearErrors
  ${GetOptions} $R0 "/SUB" $R1
  ${IfNot} ${Errors}
    StrCpy $Sub 1 ; relancé en administrateur : $INSTDIR vient de /D=
    SetSilent silent
    Return
  ${EndIf}
  ; déjà installé : on propose le même dossier (mise à jour)
  ReadRegStr $DefInstalled HKLM "${UNINST_KEY}" "InstallLocation"
  ${If} $DefInstalled == ""
    ReadRegStr $DefInstalled HKCU "${UNINST_KEY}" "InstallLocation"
  ${EndIf}
  ${If} $DefInstalled != ""
  ${AndIf} ${FileExists} "$DefInstalled\Synkro.exe"
    StrCpy $Update 1
  ${Else}
    StrCpy $DefInstalled "$PROGRAMFILES64\Synkro"
  ${EndIf}
FunctionEnd

; ---------- page « Type d'installation » ----------
Function ModePage
  ${If} $Update == 1
    !insertmacro MUI_HEADER_TEXT "Type d'installation" "${APP} est déjà installé dans $DefInstalled : il sera mis à jour (tâches et réglages gardés)."
  ${Else}
    !insertmacro MUI_HEADER_TEXT "Type d'installation" "Choisissez comment utiliser ${APP} sur ce PC."
  ${EndIf}
  nsDialogs::Create 1018
  Pop $0
  ${If} $0 == error
    Abort
  ${EndIf}
  ${NSD_CreateRadioButton} 0 0 100% 12u "Installé (recommandé)"
  Pop $hInst
  ${NSD_CreateLabel} 12u 13u -12u 20u "Dans C:\Program Files : menu Démarrer, désinstallation par Paramètres › Applications. Vos tâches et réglages sont dans %APPDATA%\Synkro."
  Pop $0
  ${NSD_CreateRadioButton} 0 38u 100% 12u "Portable (clé USB, disque externe, dossier à emporter)"
  Pop $hPort
  ${NSD_CreateLabel} 12u 51u -12u 20u "Tout reste dans le dossier choisi, tâches et réglages compris (sous-dossier synkro-data). Rien n'est inscrit dans Windows."
  Pop $0
  ${NSD_CreateCheckBox} 0 82u 100% 12u "Ajouter ${APP} au menu Démarrer"
  Pop $hSM
  ${NSD_CreateCheckBox} 0 96u 100% 12u "Créer un raccourci sur le bureau"
  Pop $hDesk
  ${NSD_CreateLabel} 0 116u 100% 20u ""
  Pop $hNote
  ${If} $Portable == 1
    ${NSD_Check} $hPort
  ${Else}
    ${NSD_Check} $hInst
  ${EndIf}
  ${If} $WantSM == 1
    ${NSD_Check} $hSM
  ${EndIf}
  ${If} $WantDesk == 1
    ${NSD_Check} $hDesk
  ${EndIf}
  ${NSD_OnClick} $hInst ModeChanged
  ${NSD_OnClick} $hPort ModeChanged
  Push 0
  Call ModeChanged
  nsDialogs::Show
FunctionEnd

Function ModeChanged
  Pop $0
  ${NSD_GetState} $hPort $1
  ${If} $1 == ${BST_CHECKED}
    ; portable : pas de menu Démarrer (le chemin change d'un PC à l'autre)
    ${NSD_Uncheck} $hSM
    EnableWindow $hSM 0
    ${NSD_Uncheck} $hDesk
    ${NSD_SetText} $hNote "Mode portable : lancez Synkro.exe depuis son dossier. Le raccourci sur le bureau ne marcherait que sur ce PC."
  ${Else}
    EnableWindow $hSM 1
    ${NSD_SetText} $hNote ""
  ${EndIf}
FunctionEnd

Function ModeLeave
  ${NSD_GetState} $hPort $0
  ${If} $0 == ${BST_CHECKED}
    StrCpy $Portable 1
  ${Else}
    StrCpy $Portable 0
  ${EndIf}
  ${NSD_GetState} $hSM $0
  ${If} $0 == ${BST_CHECKED}
    StrCpy $WantSM 1
  ${Else}
    StrCpy $WantSM 0
  ${EndIf}
  ${NSD_GetState} $hDesk $0
  ${If} $0 == ${BST_CHECKED}
    StrCpy $WantDesk 1
  ${Else}
    StrCpy $WantDesk 0
  ${EndIf}
FunctionEnd

; ---------- page « Emplacement » ----------
Function DirPre
  ; dossier par défaut adapté au mode, proposé quand le mode change
  ${If} $LastMode != $Portable
    ${If} $Portable == 1
      StrCpy $INSTDIR "$DESKTOP\Synkro"
    ${Else}
      StrCpy $INSTDIR $DefInstalled
    ${EndIf}
    StrCpy $LastMode $Portable
  ${EndIf}
FunctionEnd

Function DirShow
  ${If} $Portable == 1
    !insertmacro MUI_HEADER_TEXT "Emplacement de la version portable" "Choisissez le dossier (sur une clé USB par exemple)."
  ${Else}
    !insertmacro MUI_HEADER_TEXT "Emplacement de l'installation" "Choisissez le dossier où installer ${APP}."
  ${EndIf}
FunctionEnd

Function DirLeave
  Call Writable
  ${If} $R9 == 1
    StrCpy $NeedAdmin 0
  ${ElseIf} $Portable == 1
    MessageBox MB_ICONEXCLAMATION|MB_OK "Impossible d'écrire dans ce dossier :$\r$\n$INSTDIR$\r$\n$\r$\nPour une version portable, choisissez un dossier à vous (bureau, clé USB…)."
    Abort
  ${Else}
    StrCpy $NeedAdmin 1 ; Program Files… : Windows demandera l'autorisation
  ${EndIf}
  ; Synkro ouvert depuis ce dossier : il faut le fermer
  retry:
  Call IsRunning
  ${If} $R9 == 1
    MessageBox MB_ICONEXCLAMATION|MB_RETRYCANCEL "${APP} est ouvert (peut-être réduit près de l'horloge).$\r$\nFermez-le (clic droit sur son icône › Quitter), puis cliquez sur Recommencer." IDRETRY retry
    Abort
  ${EndIf}
  ; changement de mode dans un dossier existant
  ${If} $Portable == 0
  ${AndIf} ${FileExists} "$INSTDIR\portable"
    MessageBox MB_ICONQUESTION|MB_YESNO "Ce dossier contient une version portable de ${APP}.$\r$\n$\r$\nEn mode installé, ses tâches et réglages (sous-dossier synkro-data) ne seront plus utilisés : ils restent dans le dossier, rien n'est effacé.$\r$\n$\r$\nContinuer ?" IDYES +2
    Abort
  ${EndIf}
FunctionEnd

; ---------- installation ----------

Section "Synkro" SecMain
  ${If} $Sub == 0
  ${AndIf} $Portable == 0
  ${AndIf} $NeedAdmin == 1
    ; Program Files… : une copie de l'installateur, relancée en administrateur
    ; et sans fenêtre, copie les fichiers et inscrit Synkro (voir $Sub)
    DetailPrint "Copie dans $INSTDIR (autorisation de Windows demandée)…"
    ExecShellWait "runas" "$EXEPATH" "/S /SUB /D=$INSTDIR"
    ${IfNot} ${FileExists} "$INSTDIR\Synkro.exe"
      MessageBox MB_ICONEXCLAMATION|MB_OK "Synkro n'a pas pu être copié dans :$\r$\n$INSTDIR$\r$\n$\r$\nL'autorisation de Windows a peut-être été refusée. Relancez l'installation, ou choisissez un dossier à vous."
      Abort "Installation annulée."
    ${EndIf}
    DetailPrint "Copié."
  ${Else}
    ${If} $Sub == 1
      SetShellVarContext all ; inscription pour tous (HKLM)
    ${EndIf}
    SetOutPath "$INSTDIR"
    File "/oname=Synkro.exe" "${EXE}"
    File "LISEZMOI.txt"
    ${If} $Portable == 1
      ; le fichier « portable » range les données à côté de l'exécutable
      FileOpen $0 "$INSTDIR\portable" w
      FileWrite $0 "Ce fichier active le mode portable : les données de Synkro sont dans synkro-data.$\r$\n"
      FileClose $0
    ${Else}
      Delete "$INSTDIR\portable"
      WriteUninstaller "$INSTDIR\${UNINST_EXE}"
      ; Paramètres › Applications (SHCTX : HKLM en administrateur, sinon HKCU)
      WriteRegStr SHCTX "${UNINST_KEY}" "DisplayName" "${APP}"
      WriteRegStr SHCTX "${UNINST_KEY}" "DisplayVersion" "${VERSION}"
      WriteRegStr SHCTX "${UNINST_KEY}" "Publisher" "${APP}"
      WriteRegStr SHCTX "${UNINST_KEY}" "InstallLocation" "$INSTDIR"
      WriteRegStr SHCTX "${UNINST_KEY}" "DisplayIcon" "$INSTDIR\Synkro.exe"
      WriteRegStr SHCTX "${UNINST_KEY}" "UninstallString" '"$INSTDIR\${UNINST_EXE}"'
      WriteRegStr SHCTX "${UNINST_KEY}" "QuietUninstallString" '"$INSTDIR\${UNINST_EXE}" /S'
      WriteRegDWORD SHCTX "${UNINST_KEY}" "NoModify" 1
      WriteRegDWORD SHCTX "${UNINST_KEY}" "NoRepair" 1
      ${GetSize} "$INSTDIR" "/S=0K" $1 $2 $3
      IntFmt $1 "0x%08X" $1
      WriteRegDWORD SHCTX "${UNINST_KEY}" "EstimatedSize" "$1"
    ${EndIf}
    ${If} $Sub == 1
      Return ; l'instance visible s'occupe des raccourcis
    ${EndIf}
  ${EndIf}

  ; raccourcis : pour l'utilisateur
  ${If} $WantSM == 1
  ${AndIf} $Portable == 0
    CreateShortCut "$SMPROGRAMS\Synkro.lnk" "$INSTDIR\Synkro.exe" "" "$INSTDIR\Synkro.exe" 0
  ${EndIf}
  ${If} $WantDesk == 1
    CreateShortCut "$DESKTOP\Synkro.lnk" "$INSTDIR\Synkro.exe" "" "$INSTDIR\Synkro.exe" 0
  ${EndIf}
SectionEnd

; ---------- désinstallation ----------

Function un.onInit
  ${If} ${RunningX64}
    SetRegView 64
  ${EndIf}
  StrCpy $Sub 0
  ${GetParameters} $R0
  ClearErrors
  ${GetOptions} $R0 "/SUB" $R1
  ${IfNot} ${Errors}
    StrCpy $Sub 1
    SetSilent silent
  ${EndIf}
FunctionEnd

; supprime un raccourci seulement s'il pointe vers ce Synkro-ci
; (un raccourci vers une version portable est laissé)
Function un.DelLinkIfOurs
  Exch $0
  ${If} ${FileExists} "$0"
    nsExec::ExecToStack `powershell -NoProfile -NonInteractive -ExecutionPolicy Bypass -Command "$$t=(New-Object -ComObject WScript.Shell).CreateShortcut('$0').TargetPath; if ($$t -ieq '$INSTDIR\Synkro.exe') { 'oui' } else { 'non' }"`
    Pop $1
    Pop $2
    StrCpy $2 $2 3
    ${If} $1 != 0
    ${OrIf} $2 == "oui"
      Delete "$0" ; PowerShell indisponible : même nom, on le retire
    ${EndIf}
  ${EndIf}
  Pop $0
FunctionEnd

Section "Uninstall"
  ; instance relancée en administrateur : effacer les fichiers et l'inscription
  ${If} $Sub == 1
    Delete "$INSTDIR\Synkro.exe"
    Delete "$INSTDIR\LISEZMOI.txt"
    Delete "$INSTDIR\${UNINST_EXE}"
    RMDir "$INSTDIR"
    SetShellVarContext all
    DeleteRegKey SHCTX "${UNINST_KEY}"
    Return
  ${EndIf}

  uretry:
  Call un.IsRunning
  ${If} $R9 == 1
    MessageBox MB_ICONEXCLAMATION|MB_RETRYCANCEL "${APP} est ouvert (peut-être réduit près de l'horloge).$\r$\nFermez-le, puis cliquez sur Recommencer." IDRETRY uretry
    Abort "Désinstallation annulée : ${APP} est ouvert."
  ${EndIf}

  ; raccourcis de l'utilisateur qui visent ce Synkro-ci
  Push "$SMPROGRAMS\Synkro.lnk"
  Call un.DelLinkIfOurs
  Push "$DESKTOP\Synkro.lnk"
  Call un.DelLinkIfOurs

  ; fichiers : directement, ou par une copie relancée en administrateur
  Call un.Writable
  ${If} $R9 == 1
    Delete "$INSTDIR\Synkro.exe"
    Delete "$INSTDIR\LISEZMOI.txt"
    Delete "$INSTDIR\${UNINST_EXE}"
    RMDir "$INSTDIR"
    ; déjà administrateur (lancé par Paramètres) : l'inscription peut être côté machine
    ReadRegStr $R0 HKLM "${UNINST_KEY}" "InstallLocation"
    ${If} $R0 == $INSTDIR
      DeleteRegKey HKLM "${UNINST_KEY}"
    ${EndIf}
    ReadRegStr $R0 HKCU "${UNINST_KEY}" "InstallLocation"
    ${If} $R0 == $INSTDIR
      DeleteRegKey HKCU "${UNINST_KEY}"
    ${EndIf}
  ${Else}
    DetailPrint "Suppression dans $INSTDIR (autorisation de Windows demandée)…"
    ExecShellWait "runas" "$INSTDIR\${UNINST_EXE}" "/S /SUB"
    ; le désinstallateur travaille depuis une copie temporaire : on attend la fin
    StrCpy $3 0
    ${DoWhile} ${FileExists} "$INSTDIR\Synkro.exe"
      IntOp $3 $3 + 1
      ${If} $3 > 60
        ${Break}
      ${EndIf}
      Sleep 500
    ${Loop}
    ${If} ${FileExists} "$INSTDIR\Synkro.exe"
      MessageBox MB_ICONEXCLAMATION|MB_OK "Les fichiers de ${APP} n'ont pas pu être supprimés de :$\r$\n$INSTDIR$\r$\n$\r$\nL'autorisation de Windows a peut-être été refusée."
      Abort "Désinstallation incomplète."
    ${EndIf}
    ; le dossier vide est retiré par l'instance administrateur (attente brève)
    Sleep 1000
  ${EndIf}

  ; tâches et réglages : gardés sauf demande explicite
  ${If} ${FileExists} "$APPDATA\Synkro\*.*"
    MessageBox MB_ICONQUESTION|MB_YESNO|MB_DEFBUTTON2 "Supprimer aussi vos tâches, réglages et journaux ($APPDATA\Synkro) ?$\r$\n$\r$\nRépondez Non pour les retrouver si vous réinstallez ${APP}." /SD IDNO IDNO +2
    RMDir /r "$APPDATA\Synkro"
  ${EndIf}
SectionEnd
