// Stylesheets are imported for their side effect (Vite bundles them).
declare module '*.css';

// Text files bundled as strings (the licence and notices in About).
declare module '*?raw' {
  const text: string;
  export default text;
}
