# Classic Frontend Architecture Guide

**Project Stack**: React 18 + Vite + Semi Design UI + React Router v6 + i18next

---

## 1. Directory Structure

### Core Application Files
```
src/
├── App.jsx                 # Main routing configuration
├── index.jsx              # React app initialization with providers
├── index.css              # Global styles
├── pages/                 # Page/route components
├── components/            # Shared components
├── context/               # Context providers (User, Status, Theme)
├── helpers/               # Utility functions and API calls
├── hooks/                 # Custom React hooks
├── i18n/                  # Internationalization setup
├── constants/             # App constants
└── services/              # Service layer
```

---

## 2. Page Structure (20 Pages Total)

### Admin/Console Pages (require `AdminRoute` wrapper):
- **Channel** (`/console/channel`) - Manage API channels with complex table, modals, testing
- **User** (`/console/user`) - User management and administration
- **Subscription** (`/console/subscription`) - Subscription management
- **Redemption** (`/console/redemption`) - Redemption code management
- **Model** (`/console/models`) - Model marketplace and listing
- **ModelDeployment** (`/console/deployment`) - Model deployment management
- **Setting** (`/console/setting`) - System-wide admin settings

### Authenticated Pages (require `PrivateRoute` wrapper):
- **Dashboard** (`/console`) - Main user dashboard (lazy-loaded)
- **Token** (`/console/token`) - API token management
- **Playground** (`/console/playground`) - API testing playground with SSE support
- **TopUp** (`/console/topup`) - Account top-up/billing and subscription plans
- **Log** (`/console/log`) - User activity logs
- **Chat** (`/console/chat/:id?`) - Chat interface with history
- **Chat2Link** (`/chat2link`) - Convert chat to shareable links
- **Midjourney** (`/console/midjourney`) - Midjourney integration
- **Task** (`/console/task`) - Task management
- **PersonalSetting** (`/console/personal`) - User personal settings and preferences

### Public Pages:
- **Home** (`/`) - Landing page (lazy-loaded)
- **Setup** (`/setup`) - Initial setup/onboarding page
- **Pricing** (`/pricing`) - Pricing page (conditionally requires auth based on config)
- **About** (`/about`) - About page (lazy-loaded)
- **UserAgreement** (`/user-agreement`) - User agreement (lazy-loaded)
- **PrivacyPolicy** (`/privacy-policy`) - Privacy policy (lazy-loaded)

### Auth Pages:
- **LoginForm** (`/login`) - Login page
- **RegisterForm** (`/register`) - Registration page
- **PasswordResetForm** (`/reset`) - Password reset request
- **PasswordResetConfirm** (`/user/reset`) - Password reset confirmation token page
- **OAuth2Callback** - OAuth provider callbacks:
  - `/oauth/github` - GitHub OAuth
  - `/oauth/discord` - Discord OAuth
  - `/oauth/oidc` - OIDC provider
  - `/oauth/linuxdo` - LinuxDO OAuth
  - `/oauth/:provider` - Dynamic provider support

### Error Pages:
- **NotFound** (`*`) - 404 page
- **Forbidden** (`/forbidden`) - 403 access denied page

---

## 3. Core Layout System

### PageLayout Component (`components/layout/PageLayout.jsx`)

The main layout wrapper that orchestrates the entire application layout:

**Structure**:
```
┌─────────────────────────────────────┐
│  Header (Fixed, z-index: 100)       │ ← HeaderBar with Navigation
├──────────┬──────────────────────────┤
│  Sidebar │  Content Area            │
│ (Fixed)  │  (Scrollable)            │
│ z:99     │  ├─ Pages                │
│          │  └─ Routes               │
│          ├──────────────────────────┤
│          │  Footer                  │
└──────────┴──────────────────────────┘
```

**Key Features**:
- **Header**: Fixed position, 64px height, contains logo and navigation
- **Sidebar**: Fixed left panel (width: `var(--sidebar-current-width)`)
- **Content**: Main page area with padding on console routes
- **Footer**: Hidden on admin pages (channel, log, redemption, user, token, midjourney, task, models, pricing)
- **Mobile Responsive**: Sidebar becomes drawer, full width on mobile
- **Language Sync**: Reads user's language preference and updates i18n
- **System Status**: Fetches `/api/status` on mount

**Status Loading** (PageLayout):
```js
// Loads user from localStorage
loadUser() → UserContext dispatch

// Loads system config
API.get('/api/status') → StatusContext dispatch
```

### Header Components (`components/layout/headerbar/`)
- **HeaderBar** - Main header wrapper
- **Navigation** - Top navigation links (renders mainNavLinks)
- **HeaderLogo** - Logo/branding area

### Sidebar (`components/layout/SiderBar.jsx`)
- Dynamic menu based on user role
- Active route highlighting
- Collapse/expand functionality
- Mobile drawer support

---

## 4. Component Organization

### Key Component Categories

#### `components/table/` - Admin Data Tables
Complex data table implementations for each feature:

**channels/**
- `ChannelsTable.jsx` - Main table component with pagination
- `ChannelsColumnDefs.jsx` - Column definitions (20+ columns: id, name, type, model, key, status, etc.)
- `ChannelsActions.jsx` - Row action handlers (edit, delete, test, etc.)
- `ChannelsFilters.jsx` - Filter/search controls
- `ChannelsTabs.jsx` - Tab-based filtering
- `modals/`:
  - `EditChannelModal.jsx` - Edit channel details
  - `ModelTestModal.jsx` - Test channel with model
  - `ModelSelectModal.jsx` - Select models
  - `ColumnSelectorModal.jsx` - Show/hide columns
  - `StatusCodeRiskGuardModal.jsx` - HTTP status code rules
  - `MultiKeyManageModal.jsx` - Multi-API key management
  - `ChannelUpstreamUpdateModal.jsx` - Upstream detection
  - `CodexOAuthModal.jsx` - OAuth configuration
  - `OllamaModelModal.jsx` - Ollama model selection
  - `ParamOverrideEditorModal.jsx` - Parameter overrides
  - `EditTagModal.jsx`, `BatchTagModal.jsx` - Tag management

**Other tables** (similar pattern):
- `users/` - User management
- `tokens/` - Token CRUD
- `model-deployments/` - Deployment management
- `model-pricing/` - Pricing table with sidebar
- `subscriptions/` - Subscription data
- `redemptions/` - Redemption codes
- `usage-logs/` - Usage statistics
- `task-logs/` - Task logs
- `mj-logs/` - Midjourney logs

#### `components/settings/` - Admin Settings Panels
```
DashboardSetting.jsx          # Dashboard configuration
PersonalSetting.jsx           # User profile settings
SystemSetting.jsx             # System-wide settings
ModelSetting.jsx              # Model configuration
RateLimitSetting.jsx          # Rate limiting rules
RatioSetting.jsx              # Pricing ratios
PaymentSetting.jsx            # Payment method settings
OperationSetting.jsx          # Operational settings
PerformanceSetting.jsx        # Performance tuning
ChatsSetting.jsx              # Chat system settings
DrawingSetting.jsx            # Drawing/image settings
CustomOAuthSetting.jsx        # Custom OAuth providers
ModelDeploymentSetting.jsx    # Deployment configuration
HttpStatusCodeRulesInput.jsx  # Status code handling
ChannelSelectorModal.jsx      # Channel selection in settings
OtherSetting.jsx              # Miscellaneous settings
```

#### `components/auth/`
- `LoginForm.jsx` - Login with email/password
- `RegisterForm.jsx` - User registration
- `PasswordResetForm.jsx` - Request password reset
- `PasswordResetConfirm.jsx` - Confirm with token
- `OAuth2Callback.jsx` - Handle OAuth provider callbacks

#### `components/playground/`
API testing interface:
- `ParameterControl.jsx` - Model/group/parameter inputs
- `SettingsPanel.jsx` - Request settings configuration
- `DebugPanel.jsx` - Debug output viewer
- `SSEViewer.jsx` - Server-sent events streaming view
- `MessageActions.jsx` - Message operations (copy, delete, etc.)
- `ThinkingContent.jsx` - Extended thinking display
- `FloatingButtons.jsx` - Action buttons UI
- `ImageUrlInput.jsx` - Image URL input for vision models

#### `components/common/ui/`
Reusable UI building blocks:
- `CardTable.jsx` - Styled table wrapper
- `Loading.jsx` - Loading spinner
- `Modal.jsx` - Modal dialogs
- `Button.jsx` - Button variants
- Various Semi Design wrappers

#### `components/topup/`
Top-up/subscription UI:
- `RechargeCard.jsx` - Top-up card component
- `SubscriptionPlansCard.jsx` - Subscription plan cards
- `InvitationCard.jsx` - Referral/invitation cards

---

## 5. State Management & Context

### Three Primary Contexts

#### **UserContext** (`context/User/`)
**State Structure**:
```js
{
  user: {
    id: string,
    username: string,
    email: string,
    role: 'user' | 'admin',
    group: string,
    status: 'active' | 'inactive' | 'banned',
    setting: string (JSON), // user preferences
    quota: {
      used: number,
      hard_limit: number,
      month_quota: number,
      day_quota: number,
      ...
    }
  },
  loginStatus: boolean
}
```

**Actions** (via reducer):
- `login(payload)` - Set user data on login
- `logout()` - Clear user data
- `update(payload)` - Update user fields
- `setQuota(payload)` - Update quota info

**Usage**:
```js
const [userState, dispatch] = useContext(UserContext);
dispatch({ type: 'login', payload: userData });
if (userState.user?.role === 'admin') { /* ... */ }
```

#### **StatusContext** (`context/Status/`)
**State Structure**:
```js
{
  status: {
    SystemName: string,              // Site title
    Logo: string (URL),              // Favicon/logo
    FooterHTML: string,              // Footer content
    HeaderNavModules: string (JSON), // Header nav config
    SMTPServer: string,
    SMTPPort: number,
    SMTPFromAddr: string,
    // ... 20+ other system settings
  }
}
```

**Purpose**: Store system configuration fetched from `/api/status`

#### **ThemeContext** (`context/Theme/`)
```js
{
  isDark: boolean,
  toggleTheme: () => void
}
```

**Features**:
- Reads browser dark mode preference
- Syncs with localStorage
- Updates CSS variables

---

## 6. API Architecture

### Axios Instance Setup (`helpers/api.js`)

```js
export const API = axios.create({
  baseURL: import.meta.env.VITE_REACT_APP_SERVER_URL || '',
  headers: {
    'New-API-User': getUserIdFromLocalStorage(),
    'Cache-Control': 'no-store',
  }
});
```

**Features**:
1. **Request Deduplication**: GET requests with same URL + params are deduped
2. **Global Error Handler**: Catches errors and shows toast via `showError()`
3. **User Header**: Auto-includes user ID in every request
4. **No Caching**: `Cache-Control: no-store` header

### Common API Patterns

**OAuth Functions** (`helpers/api.js`):
- `getOAuthState()` - Get CSRF state for OAuth flow
- `onGitHubOAuthClicked(client_id)` - Initiate GitHub OAuth
- `onDiscordOAuthClicked(client_id)` - Initiate Discord OAuth
- `onOIDCClicked(auth_url, client_id)` - Generic OIDC provider
- `onLinuxDOOAuthClicked(client_id)` - LinuxDO OAuth
- `onCustomOAuthClicked(provider)` - Custom OAuth provider

**Model/Group Processing**:
- `processModelsData(data, currentModel)` - Format model options
- `processGroupsData(data, userGroup)` - Format group options with ratio info

**Playground**:
- `buildApiPayload(messages, systemPrompt, inputs, parameterEnabled)` - Build request
- `handleApiError(error, response)` - Format error for display

**Channel Models**:
- `loadChannelModels()` - Fetch `/api/models` and cache
- `getChannelModels(type)` - Get cached models by type

### Error Handling Pattern
```js
try {
  const res = await API.get('/api/endpoint');
  const { success, data } = res.data;
  if (success) {
    // Use data
  } else {
    showError(message); // Show error toast
  }
} catch (error) {
  // Error already shown by global handler
  // Or use skipErrorHandler config to handle manually
  const res = await API.get('/api/...', { skipErrorHandler: true });
}
```

---

## 7. Internationalization (i18n)

### Setup Files

**`i18n/i18n.js`**:
```js
i18n
  .use(LanguageDetector)  // Auto-detect from browser
  .use(initReactI18next)
  .init({
    load: 'currentOnly',
    supportedLngs: ['en', 'zh-CN', 'zh-TW', 'fr', 'ru', 'ja', 'vi'],
    resources: { /* import all locales */ },
    fallbackLng: 'zh-CN',
    nsSeparator: false,  // Flat key structure
    interpolation: { escapeValue: false }
  });
```

**`i18n/language.js`**:
```js
normalizeLanguage(language)
// Handles: zh → zh-CN, zh-tw → zh-TW, zh-HK → zh-TW, etc.
```

**Translation Files** (`i18n/locales/`):
- `en.json` - English
- `zh-CN.json` - Simplified Chinese (default)
- `zh-TW.json` - Traditional Chinese
- `fr.json` - French
- `ru.json` - Russian
- `ja.json` - Japanese
- `vi.json` - Vietnamese

**Key Structure**: Flat namespace with dot-notation paths
```json
{
  "channels.title": "Channel Management",
  "channels.add": "Add Channel",
  "dashboard.title": "Dashboard",
  ...
}
```

### Usage in Components

```jsx
const { t, i18n } = useTranslation();

// In JSX
return (
  <>
    <h1>{t('dashboard.title')}</h1>
    <select onChange={e => i18n.changeLanguage(e.target.value)}>
      <option value='en'>English</option>
      <option value='zh-CN'>中文</option>
    </select>
  </>
);
```

### Language Preference Sync
**PageLayout.jsx** syncs:
1. User's stored preference (from `user.setting` JSON)
2. Fallback to browser localStorage `i18nextLng`
3. Updates i18n instance on mount and when user changes

---

## 8. Routing & Authorization

### Route Configuration (`App.jsx`)

**Three Route Guard Types**:

1. **PrivateRoute** - Requires authenticated user
   ```jsx
   <PrivateRoute>
     <Component />
   </PrivateRoute>
   ```
   Checks: `userState.loginStatus === true`

2. **AdminRoute** - Requires admin role
   ```jsx
   <AdminRoute>
     <Component />
   </AdminRoute>
   ```
   Checks: `userState.user?.role === 'admin'`

3. **AuthRedirect** - Redirects logged-in users to `/console`
   ```jsx
   <AuthRedirect>
     <LoginForm />
   </AuthRedirect>
   ```
   Prevents auth pages when already logged in

**Code Splitting** (lazy loading):
- Lazy: Home, Dashboard, About, UserAgreement, PrivacyPolicy
- Pre-loaded: Channel, Token, Playground, etc.

**Route Priority**:
1. Specific paths (`/login`, `/register`, etc.)
2. Console routes (`/console/...`)
3. Public routes (`/pricing`, `/about`, etc.)
4. Wildcard `*` → NotFound

---

## 9. Common Implementation Patterns

### Admin Table Pattern (e.g., Channels)

**Architecture**:
```
ChannelsPage (simple wrapper)
  └─ ChannelsContainer (state management)
     ├─ ChannelsTable (UI rendering)
     ├─ ChannelsFilters (search/filter UI)
     ├─ ChannelsTabs (tab UI)
     ├─ Modals:
     │  ├─ EditChannelModal
     │  ├─ ModelTestModal
     │  ├─ ModelSelectModal
     │  └─ ... (other modals)
     └─ API calls:
        ├─ GET /api/channels
        ├─ POST /api/channels
        ├─ PUT /api/channels/{id}
        └─ DELETE /api/channels/{id}
```

**State in Container**:
```js
const [channels, setChannels] = useState([]);
const [loading, setLoading] = useState(false);
const [page, setPage] = useState(1);
const [pageSize, setPageSize] = useState(10);
const [total, setTotal] = useState(0);
const [selectedChannels, setSelectedChannels] = useState([]);
const [editingChannel, setEditingChannel] = useState(null);
const [showEditModal, setShowEditModal] = useState(false);
```

**Data Fetching**:
```js
const fetchChannels = useCallback(async () => {
  setLoading(true);
  try {
    const res = await API.get('/api/channels', {
      params: { page, page_size: pageSize, search: searchText }
    });
    const { success, data, message } = res.data;
    if (success) {
      setChannels(data.items);
      setTotal(data.total);
    } else {
      showError(message);
    }
  } catch (error) {
    // Handled by global error handler
  } finally {
    setLoading(false);
  }
}, [page, pageSize, searchText]);

useEffect(() => {
  fetchChannels();
}, [fetchChannels]);
```

### Modal Pattern

```jsx
const [visible, setVisible] = useState(false);
const [data, setData] = useState(null);

const openModal = (record) => {
  setData(record);
  setVisible(true);
};

const handleSubmit = async (formData) => {
  try {
    const res = await API.put(`/api/channels/${data.id}`, formData);
    if (res.data.success) {
      showSuccess('Updated successfully');
      setVisible(false);
      fetchChannels(); // Refresh list
    }
  } catch (error) {
    // Handled by global error
  }
};

return (
  <>
    <Modal visible={visible} onCancel={() => setVisible(false)} title='Edit Channel'>
      <Form onSubmit={handleSubmit} initialValues={data} />
    </Modal>
  </>
);
```

### Hook Pattern

```js
// hooks/channels/useChannels.js
export const useChannels = () => {
  const [channels, setChannels] = useState([]);
  const [loading, setLoading] = useState(false);
  
  const fetch = useCallback(async () => {
    setLoading(true);
    try {
      const res = await API.get('/api/channels');
      setChannels(res.data.data);
    } finally {
      setLoading(false);
    }
  }, []);
  
  useEffect(() => {
    fetch();
  }, [fetch]);
  
  return { channels, loading, refetch: fetch };
};

// Usage in component
const { channels, loading, refetch } = useChannels();
```

---

## 10. Custom Hooks Library

Organized in `/hooks/` by feature:

**common/**
- `useIsMobile()` - Detect mobile viewport (media query)
- `useSidebarCollapsed()` - Sidebar state with localStorage persistence
- `useDebounce()` - Debounce hook for search inputs
- `useAsync()` - Handle async operations with loading/error states
- `usePagination()` - Pagination state management
- `useLocalStorage()` - Persist state to localStorage

**Feature-specific** (channels/, chat/, dashboard/, tokens/, etc.):
- Data fetching hooks
- State management hooks
- Event handlers

---

## 11. Environment & Build Configuration

### Environment Variables (`.env`)
```
VITE_REACT_APP_SERVER_URL=http://localhost:8000
VITE_REACT_APP_API_BASE=/api
```

**Access in code**:
```js
import.meta.env.VITE_REACT_APP_SERVER_URL
```

### Build System (Vite)

**Commands**:
- `npm run dev` - Start dev server (port 5173)
- `npm run build` - Production build → `dist/`
- `npm run preview` - Preview production build
- `npm run lint` - Run linter

**Vite Features**:
- Fast HMR (hot module replacement)
- CSS imports and processing
- Asset optimization
- Dynamic imports for code splitting

---

## 12. Style System

### CSS Architecture

**Global Styles** (`index.css`):
- Tailwind CSS utility classes
- CSS variables for theme colors
- Semi Design component styles import

**Component Styles**:
- **Inline styles**: For dynamic/responsive values
- **Tailwind classes**: For static utility styles
- **CSS modules**: Occasionally for scoped styles
- **Semi Design**: Theme-aware component styling

**CSS Variables** (theme-aware):
```css
--sidebar-current-width: 200px | 64px (collapsed)
--semi-color-primary: Dynamic theme color
--semi-color-bg-base: Background color (light/dark)
```

**Responsive Breakpoints** (Tailwind):
- `sm`: 640px
- `md`: 768px
- `lg`: 1024px
- `xl`: 1280px

---

## 13. Component Library: Semi Design

**Version**: Latest @douyinfe/semi-ui

**Common Components Used**:
- `Layout` - Page layout (Header, Sider, Content, Footer)
- `Table` - Data tables
- `Form` - Form input handling
- `Modal` - Dialog modals
- `Button` - Button variants
- `Input` - Text input
- `Select` - Dropdown select
- `DatePicker` - Date selection
- `Checkbox`, `Radio` - Form controls
- `Toast` - Success/error notifications
- `Tooltip` - Hover tooltips
- `Tag` - Label tags
- `Card` - Content cards

**Locale Integration**:
- Semi UI locale provider wraps app
- Language syncs with i18next
- Supports `zh_CN` and `en_GB` locales

---

## 14. Key Dependencies

```json
{
  "react": "^18.x",
  "react-dom": "^18.x",
  "react-router-dom": "^6.x",
  "@douyinfe/semi-ui": "^2.x",
  "axios": "^1.x",
  "i18next": "^23.x",
  "react-i18next": "^13.x",
  "react-toastify": "^9.x",
  "tailwindcss": "^3.x"
}
```

---

## 15. File Structure Summary

```
web/classic/
├── src/
│   ├── App.jsx                 # Routes
│   ├── index.jsx              # App providers
│   ├── index.css              # Global styles
│   ├── pages/                 # Page components (20 files)
│   ├── components/
│   │   ├── layout/            # Layout (PageLayout, Header, Sidebar)
│   │   ├── auth/              # Auth forms
│   │   ├── table/             # Data tables (channels, users, tokens, etc.)
│   │   ├── settings/          # Settings components
│   │   ├── playground/        # Playground UI
│   │   ├── common/            # Reusable UI components
│   │   └── topup/             # Top-up UI
│   ├── context/
│   │   ├── User/              # User state
│   │   ├── Status/            # System config
│   │   └── Theme/             # Dark/light theme
│   ├── helpers/               # Utility functions and API
│   ├── hooks/                 # Custom React hooks
│   ├── i18n/
│   │   ├── i18n.js            # i18next config
│   │   ├── language.js        # Language helpers
│   │   └── locales/           # Translation JSON files
│   ├── constants/             # App constants
│   └── services/              # Service layer
├── public/                    # Static assets
├── index.html                 # HTML entry
├── vite.config.js            # Vite configuration
└── package.json              # Dependencies
```

---

## 16. Development Workflow

### Adding a New Feature

1. **Create page** → `pages/NewFeature/index.jsx`
2. **Create table component** → `components/table/new-features/`
3. **Add route** → `App.jsx` with appropriate guard
4. **Add sidebar link** → `SiderBar.jsx`
5. **Add translations** → `i18n/locales/*.json`
6. **Create hooks** → `hooks/new-features/useNewFeature.js`
7. **Create API helpers** → `helpers/newFeature.js` or service file

### Adding an API Endpoint Call

```js
// In component or hook
const res = await API.get('/api/endpoint', {
  params: { search, page, limit }
});

const { success, data, message } = res.data;
if (success) {
  // Handle data
} else {
  showError(message);
}
```

### Adding Translation

1. Add key to all locale files:
   ```json
   // i18n/locales/en.json
   { "feature.title": "My Feature" }
   
   // i18n/locales/zh-CN.json
   { "feature.title": "我的功能" }
   ```

2. Use in component:
   ```jsx
   const { t } = useTranslation();
   <h1>{t('feature.title')}</h1>
   ```

---

## 17. Performance Optimizations

- **Code splitting**: Lazy-loaded pages
- **Request deduplication**: GET request dedup in API instance
- **React.memo**: Component memoization for heavy tables
- **useCallback**: Dependency optimization
- **useMemo**: Expensive computation caching
- **Virtual scrolling**: For large lists (if implemented)

---

## 18. Security Considerations

- **User ID Header**: Every API request includes user ID
- **CORS**: Handled server-side
- **OAuth**: Multiple provider support with state validation
- **localStorage**: Stores user object and auth state
- **No API keys in frontend**: Keys managed server-side
- **Token management**: JWT or session-based (server-side)

---

## Summary

This is a **professional, enterprise-grade React dashboard** designed for:
- **Complex admin interfaces** with data tables and modals
- **Multi-language support** across 7 languages
- **Role-based access control** (user/admin)
- **Real-time updates** (via API polling or WebSockets)
- **Responsive design** for mobile and desktop
- **Modular architecture** for scalability

The codebase follows React best practices with clear separation of concerns between pages, components, hooks, and helpers.
