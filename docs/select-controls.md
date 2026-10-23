# Custom select controls (0.17.1)

All nine native HTML select fields now use `components/Select.tsx`: history filtering, snapshot source, plan mode/frequency, rule action, file-size units, exception action, onboarding plan and index rescan plan.

The trigger exposes combobox/listbox semantics and an active descendant. Arrow keys, Home/End, Enter/Space, Escape, Tab and prefix typing are supported. Options use green selection, bounded scrolling and viewport-aware placement. The popup is portaled into the enclosing dialog (or body on ordinary pages), keeping options accessible inside modal dialogs while avoiding scroll-container clipping.

Verified in the isolated local service: history selection via ArrowDown/Enter, Escape without change, restore popup appearance, plan mode pointer selection, frequency numeric value selection and Tab dismissal/focus progression. TypeScript/Vite and Prettier passed. No backup data operations or engine changes are part of this release.
