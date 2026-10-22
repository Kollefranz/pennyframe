; Inno Setup script. Build: iscc /DVersion=1.2.3 installer\pennyframe.iss  (see scripts/package-windows.ps1)
#ifndef Version
  #define Version "0.0.0"
#endif

[Setup]
; Never change AppId: it is how a new installer recognises (and upgrades) an existing install.
AppId={{A4E27671-342B-4042-90BB-4C28FFE3B75B}
AppName=Pennyframe
AppVersion={#Version}
AppPublisher=Kollefranz
AppPublisherURL=https://github.com/Kollefranz/pennyframe
AppSupportURL=https://github.com/Kollefranz/pennyframe/issues
AppUpdatesURL=https://github.com/Kollefranz/pennyframe/releases
; Per-user install: no admin prompt, lands in %LOCALAPPDATA%\Programs\Pennyframe.
PrivilegesRequired=lowest
DefaultDirName={autopf}\Pennyframe
DisableProgramGroupPage=yes
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
; Close a running Pennyframe instead of failing on the locked exe.
CloseApplications=yes
RestartApplications=no
SetupIconFile=..\build\windows\icon.ico
UninstallDisplayIcon={app}\pennyframe.exe
WizardStyle=modern
Compression=lzma2
SolidCompression=yes
OutputDir=..\dist
OutputBaseFilename=pennyframe-{#Version}-windows-setup

[Tasks]
Name: "desktopicon"; Description: "Create a &desktop shortcut"; GroupDescription: "Shortcuts:"

[Files]
Source: "..\build\bin\pennyframe.exe"; DestDir: "{app}"; Flags: ignoreversion

[Icons]
Name: "{autoprograms}\Pennyframe"; Filename: "{app}\pennyframe.exe"
Name: "{autodesktop}\Pennyframe"; Filename: "{app}\pennyframe.exe"; Tasks: desktopicon

[Run]
Filename: "{app}\pennyframe.exe"; Description: "Launch Pennyframe"; Flags: nowait postinstall skipifsilent
