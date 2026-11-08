// Globals the desktop app (Wails) provides, for the JavaScript type check.
import type { Backend } from './backend';

declare global {
  interface Window {
    go: { core: { App: Backend } };
    runtime: {
      EventsOn(name: string, cb: (data: any) => void): () => void;
      EventsOff(name: string): void;
      ClipboardSetText(text: string): Promise<boolean>;
      ClipboardGetText(): Promise<string>;
      BrowserOpenURL(url: string): void;
    };
  }
}
export {};
